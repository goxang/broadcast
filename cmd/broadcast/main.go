// Command broadcast runs the Broadcast controller and data plane in a single
// process. The controller watches Broadcast, Service, and EndpointSlice objects
// and publishes the resolved target set to an in-memory resolver; the proxy
// reads that resolver to fan HTTP requests out to every ready endpoint.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/goxang/broadcast/api/networking/v1alpha1"
	"github.com/goxang/broadcast/pkg/clientset"
	"github.com/goxang/broadcast/pkg/controller"
	"github.com/goxang/broadcast/pkg/metrics"
	"github.com/goxang/broadcast/pkg/proxy"
	"github.com/goxang/broadcast/pkg/resolver"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		kubeconfig   string
		namespace    string
		listenAddr   string
		workers      int
		resyncPeriod time.Duration
		maxBodyBytes int64
		logJSON      bool
	)
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig. Empty uses in-cluster config.")
	flag.StringVar(&namespace, "namespace", os.Getenv("POD_NAMESPACE"), "Namespace to watch. Empty watches all namespaces.")
	flag.StringVar(&listenAddr, "listen-addr", ":8080", "HTTP listen address for the proxy and health/metrics endpoints.")
	flag.IntVar(&workers, "workers", 2, "Number of reconciliation workers.")
	flag.DurationVar(&resyncPeriod, "resync-period", 10*time.Minute, "Informer resync period.")
	flag.Int64Var(&maxBodyBytes, "max-body-bytes", 1<<20, "Maximum broadcast request body size in bytes.")
	flag.BoolVar(&logJSON, "log-json", true, "Emit structured JSON logs.")
	flag.Parse()

	logger := newLogger(logJSON)
	slog.SetDefault(logger)

	if namespace == "" {
		logger.Warn("no namespace configured: the proxy routes broadcasts by name in a single namespace; set --namespace (or POD_NAMESPACE) so broadcasts resolve correctly")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := restConfig(kubeconfig)
	if err != nil {
		return fmt.Errorf("building rest config: %w", err)
	}

	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("building kubernetes client: %w", err)
	}
	broadcastClient, err := clientset.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("building broadcast client: %w", err)
	}

	// Broadcast informer (namespace-scoped when --namespace is set).
	bc := broadcastClient.Broadcasts(namespace)
	listWatch := &cache.ListWatch{
		ListFunc: func(opts metav1.ListOptions) (runtime.Object, error) {
			return bc.List(ctx, opts)
		},
		WatchFunc: func(opts metav1.ListOptions) (watch.Interface, error) {
			return bc.Watch(ctx, opts)
		},
	}
	broadcastInformer := cache.NewSharedIndexInformer(listWatch, &v1alpha1.Broadcast{}, resyncPeriod, cache.Indexers{})

	factory := informers.NewSharedInformerFactoryWithOptions(kubeClient, resyncPeriod, informers.WithNamespace(namespace))
	serviceInformer := factory.Core().V1().Services()
	endpointSliceInformer := factory.Discovery().V1().EndpointSlices()

	res := resolver.New()

	ctrl := controller.New(
		broadcastClient,
		broadcastInformer,
		serviceInformer.Lister(),
		serviceInformer.Informer(),
		endpointSliceInformer.Lister(),
		endpointSliceInformer.Informer(),
		res,
		logger,
	)

	m := metrics.New(func() float64 {
		var n int
		for _, s := range res.Snapshot() {
			n += len(s.Endpoints)
		}
		return float64(n)
	})

	p := proxy.New(proxy.Options{
		Resolver:     res,
		Namespace:    namespace,
		Metrics:      m,
		MaxBodyBytes: maxBodyBytes,
		Logger:       logger,
	})

	mux := http.NewServeMux()
	mux.Handle("/broadcast/", p)
	mux.Handle("/metrics", promhttp.HandlerFor(m.Registry(), promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if ctrl.Ready() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok\n"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready\n"))
	})

	server := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Start informers and controller.
	factory.Start(ctx.Done())
	go broadcastInformer.Run(ctx.Done())
	go func() {
		if err := ctrl.Run(ctx, workers); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("controller exited", "error", err)
			stop()
		}
	}()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("serving HTTP", "addr", listenAddr, "namespace", orAll(namespace))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutting down")
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	p.Close()
	return nil
}

func newLogger(json bool) *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if json {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}

func restConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	return rest.InClusterConfig()
}

func orAll(ns string) string {
	if ns == "" {
		return "(all)"
	}
	return ns
}
