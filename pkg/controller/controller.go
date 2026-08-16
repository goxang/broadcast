// Package controller reconciles Broadcast resources against the referenced
// Service and its EndpointSlices, and publishes the resolved target set into an
// in-memory resolver for the proxy. It never makes Kubernetes API calls on the
// proxy's request path; reconciliation happens on informer events only.
package controller

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	corelisters "k8s.io/client-go/listers/core/v1"
	discoverylisters "k8s.io/client-go/listers/discovery/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"github.com/goxang/broadcast/api/networking/v1alpha1"
	"github.com/goxang/broadcast/pkg/clientset"
	"github.com/goxang/broadcast/pkg/listers"
	"github.com/goxang/broadcast/pkg/resolver"
)

const (
	// reasonServiceNotFound is set when the referenced Service does not exist.
	reasonServiceNotFound = "ServiceNotFound"
	// reasonNoReadyEndpoints is set when the Service exists but resolves to no
	// ready endpoints.
	reasonNoReadyEndpoints = "NoReadyEndpoints"
	// reasonReady is set when at least one ready endpoint is resolved.
	reasonReady = "Ready"
)

// Controller reconciles Broadcasts.
type Controller struct {
	broadcastClient clientset.Interface

	broadcastLister     listers.BroadcastLister
	serviceLister       corelisters.ServiceLister
	endpointSliceLister discoverylisters.EndpointSliceLister

	resolver resolver.Resolver

	broadcastInformerSynced     cache.InformerSynced
	serviceInformerSynced       cache.InformerSynced
	endpointSliceInformerSynced cache.InformerSynced

	queue workqueue.TypedRateLimitingInterface[string]
	log   *slog.Logger

	ready atomic.Bool
}

// New builds a Controller.
func New(
	broadcastClient clientset.Interface,
	broadcastInformer cache.SharedIndexInformer,
	serviceLister corelisters.ServiceLister,
	serviceInformer cache.SharedIndexInformer,
	endpointSliceLister discoverylisters.EndpointSliceLister,
	endpointSliceInformer cache.SharedIndexInformer,
	res resolver.Resolver,
	log *slog.Logger,
) *Controller {
	if log == nil {
		log = slog.Default()
	}

	c := &Controller{
		broadcastClient:             broadcastClient,
		broadcastLister:             listers.NewBroadcastLister(broadcastInformer.GetIndexer()),
		serviceLister:               serviceLister,
		endpointSliceLister:         endpointSliceLister,
		resolver:                    res,
		broadcastInformerSynced:     broadcastInformer.HasSynced,
		serviceInformerSynced:       serviceInformer.HasSynced,
		endpointSliceInformerSynced: endpointSliceInformer.HasSynced,
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: "broadcasts"},
		),
		log: log,
	}

	// Broadcast events enqueue the affected Broadcast.
	_, _ = broadcastInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if key, ok := metaNamespaceKey(obj); ok {
				c.queue.Add(key)
			}
		},
		UpdateFunc: func(_, obj interface{}) {
			if key, ok := metaNamespaceKey(obj); ok {
				c.queue.Add(key)
			}
		},
		DeleteFunc: func(obj interface{}) {
			if key, ok := metaNamespaceKey(obj); ok {
				c.queue.Add(key)
			}
		},
	})

	// Service and EndpointSlice changes affect every Broadcast, so enqueue all.
	_, _ = serviceInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(interface{}) { c.enqueueAll() },
		UpdateFunc: func(_, _ interface{}) { c.enqueueAll() },
		DeleteFunc: func(interface{}) { c.enqueueAll() },
	})
	_, _ = endpointSliceInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(interface{}) { c.enqueueAll() },
		UpdateFunc: func(_, _ interface{}) { c.enqueueAll() },
		DeleteFunc: func(interface{}) { c.enqueueAll() },
	})

	return c
}

func metaNamespaceKey(obj interface{}) (string, bool) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		return "", false
	}
	return key, true
}

func (c *Controller) enqueueAll() {
	bs, err := c.broadcastLister.List(labels.Everything())
	if err != nil {
		runtime.HandleError(err)
		return
	}
	for _, b := range bs {
		c.queue.Add(b.Namespace + "/" + b.Name)
	}
}

// Run starts the workers, waits for cache sync, and processes the queue until
// ctx is cancelled. The informers are started by the caller.
func (c *Controller) Run(ctx context.Context, workers int) error {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	if workers < 1 {
		workers = 1
	}

	if !cache.WaitForCacheSync(ctx.Done(), c.broadcastInformerSynced, c.serviceInformerSynced, c.endpointSliceInformerSynced) {
		return fmt.Errorf("failed to sync controller caches")
	}
	c.ready.Store(true)

	// Reconcile everything once after sync so the resolver reflects initial state.
	c.enqueueAll()

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wait.UntilWithContext(ctx, c.runWorker, time.Second)
		}()
	}

	c.log.Info("controller started", "workers", workers)
	<-ctx.Done()

	// Shut the queue down before waiting for workers so that the blocking
	// queue.Get() in each worker unblocks and the workers exit. Without this,
	// workers would block forever and wg.Wait() would never return.
	c.queue.ShutDown()
	wg.Wait()
	return nil
}

func (c *Controller) runWorker(ctx context.Context) {
	for c.processNextItem(ctx) {
	}
}

// Ready reports whether the informer caches have synced and reconciliation is
// running. Used by the readiness probe.
func (c *Controller) Ready() bool {
	return c.ready.Load()
}

func (c *Controller) processNextItem(ctx context.Context) bool {
	key, shutdown := c.queue.Get()
	if shutdown {
		return false
	}
	defer c.queue.Done(key)

	if err := c.reconcile(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("reconciling %q: %w", key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

// reconcile resolves the target endpoint set for a single Broadcast and writes
// it to the resolver, then updates the Broadcast status.
func (c *Controller) reconcile(ctx context.Context, key string) error {
	ns, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}
	nn := types.NamespacedName{Namespace: ns, Name: name}

	bc, err := c.broadcastLister.Broadcasts(ns).Get(name)
	if err != nil {
		return err
	}
	if bc == nil {
		// Broadcast deleted: drop its resolver state.
		c.resolver.Delete(nn)
		return nil
	}

	state := resolver.State{
		Timeout:     bc.Spec.TimeoutDuration(),
		Concurrency: bc.Spec.ConcurrencyValue(),
	}

	svc, err := c.serviceLister.Services(ns).Get(bc.Spec.Service.Name)
	if apierrors.IsNotFound(err) {
		svc = nil
	} else if err != nil {
		return err
	}
	if svc == nil {
		c.resolver.Set(nn, state)
		return c.updateStatus(ctx, bc, 0, false, reasonServiceNotFound,
			fmt.Sprintf("service %q not found", bc.Spec.Service.Name))
	}

	slices, err := c.endpointSliceLister.EndpointSlices(ns).List(labels.Set{
		discoveryv1.LabelServiceName: svc.Name,
	}.AsSelector())
	if err != nil {
		return err
	}

	eps := resolveEndpoints(bc.Spec.Service.TargetPort, slices)
	state.Endpoints = eps
	c.resolver.Set(nn, state)

	ready := len(eps) > 0
	reason := reasonNoReadyEndpoints
	msg := "no ready endpoints"
	if ready {
		reason = reasonReady
		msg = fmt.Sprintf("resolved %d ready endpoint(s)", len(eps))
	}
	return c.updateStatus(ctx, bc, int32(len(eps)), ready, reason, msg)
}

// resolveEndpoints extracts ready, non-terminating endpoints from the
// EndpointSlices, matching the given target port.
func resolveEndpoints(port int32, slices []*discoveryv1.EndpointSlice) []resolver.Endpoint {
	var out []resolver.Endpoint
	seen := make(map[string]struct{})
	for _, slice := range slices {
		targetPort := port
		found := false
		for _, p := range slice.Ports {
			if p.Port != nil && *p.Port == port {
				targetPort = *p.Port
				found = true
				break
			}
		}
		if !found {
			// This slice does not carry the requested target port.
			continue
		}
		for _, ep := range slice.Endpoints {
			if ep.Conditions.Ready == nil || !*ep.Conditions.Ready {
				continue
			}
			if ep.Conditions.Terminating != nil && *ep.Conditions.Terminating {
				continue
			}
			for _, addr := range ep.Addresses {
				id := fmt.Sprintf("%s:%d", addr, targetPort)
				if _, dup := seen[id]; dup {
					continue
				}
				seen[id] = struct{}{}
				out = append(out, resolver.Endpoint{IP: addr, Port: targetPort})
			}
		}
	}
	return out
}

// updateStatus updates the Broadcast status if it changed.
func (c *Controller) updateStatus(ctx context.Context, bc *v1alpha1.Broadcast, endpoints int32, ready bool, reason, msg string) error {
	condStatus := metav1.ConditionTrue
	if !ready {
		condStatus = metav1.ConditionFalse
	}
	cond := metav1.Condition{
		Type:               string(v1alpha1.BroadcastReady),
		Status:             condStatus,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: bc.Generation,
		LastTransitionTime: metav1.Now(),
	}

	// Short-circuit when nothing changed to avoid status-update churn.
	if bc.Status.Endpoints == endpoints &&
		bc.Status.ObservedGeneration == bc.Generation &&
		conditionEqual(bc.Status.Conditions, cond) {
		return nil
	}

	updated := bc.DeepCopy()
	updated.Status.Endpoints = endpoints
	updated.Status.ObservedGeneration = bc.Generation
	meta.SetStatusCondition(&updated.Status.Conditions, cond)

	_, err := c.broadcastClient.Broadcasts(bc.Namespace).UpdateStatus(ctx, updated, metav1.UpdateOptions{})
	return err
}

// conditionEqual reports whether conditions already contain an equivalent
// Ready condition (same status/reason/message and observed generation).
func conditionEqual(conds []metav1.Condition, want metav1.Condition) bool {
	for _, c := range conds {
		if c.Type != want.Type {
			continue
		}
		return c.Status == want.Status &&
			c.Reason == want.Reason &&
			c.Message == want.Message &&
			c.ObservedGeneration == want.ObservedGeneration
	}
	return false
}
