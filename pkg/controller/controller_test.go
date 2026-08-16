package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	corelisters "k8s.io/client-go/listers/core/v1"
	discoverylisters "k8s.io/client-go/listers/discovery/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/goxang/broadcast/api/networking/v1alpha1"
	"github.com/goxang/broadcast/pkg/clientset"
	"github.com/goxang/broadcast/pkg/listers"
	"github.com/goxang/broadcast/pkg/resolver"
)

func boolPtr(b bool) *bool { return &b }

func endpointSlice(name, serviceName string, port int32, endpoints ...discoveryv1.Endpoint) *discoveryv1.EndpointSlice {
	p := port
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "test",
			Labels:    map[string]string{discoveryv1.LabelServiceName: serviceName},
		},
		Ports:     []discoveryv1.EndpointPort{{Port: &p}},
		Endpoints: endpoints,
	}
}

func readyEndpoint(addr string, ready bool) discoveryv1.Endpoint {
	return discoveryv1.Endpoint{
		Addresses:  []string{addr},
		Conditions: discoveryv1.EndpointConditions{Ready: boolPtr(ready)},
	}
}

func terminatingEndpoint(addr string) discoveryv1.Endpoint {
	return discoveryv1.Endpoint{
		Addresses: []string{addr},
		Conditions: discoveryv1.EndpointConditions{
			Ready:       boolPtr(true),
			Terminating: boolPtr(true),
		},
	}
}

func TestResolveEndpoints(t *testing.T) {
	slices := []*discoveryv1.EndpointSlice{
		endpointSlice("s1", "svc", 8080,
			readyEndpoint("10.0.0.1", true),
			readyEndpoint("10.0.0.2", false), // not ready: skip
			terminatingEndpoint("10.0.0.3"),  // terminating: skip
		),
		endpointSlice("s2", "svc", 9090, // wrong port: skip whole slice
			readyEndpoint("10.0.0.4", true),
		),
		endpointSlice("s3", "svc", 8080,
			readyEndpoint("10.0.0.1", true), // duplicate: dedup
			readyEndpoint("10.0.0.5", true),
		),
	}

	got := resolveEndpoints(8080, slices)
	want := map[string]bool{"10.0.0.1": true, "10.0.0.5": true}
	if len(got) != len(want) {
		t.Fatalf("resolved %d endpoints, want %d: %+v", len(got), len(want), got)
	}
	for _, ep := range got {
		if !want[ep.IP] {
			t.Fatalf("unexpected endpoint %s", ep.IP)
		}
		if ep.Port != 8080 {
			t.Fatalf("port = %d, want 8080", ep.Port)
		}
	}
}

func TestResolveEndpointsSkipsEmptyAddresses(t *testing.T) {
	slices := []*discoveryv1.EndpointSlice{
		endpointSlice("s1", "svc", 8080,
			discoveryv1.Endpoint{Addresses: nil, Conditions: discoveryv1.EndpointConditions{Ready: boolPtr(true)}},
			readyEndpoint("10.0.0.1", true),
		),
	}
	got := resolveEndpoints(8080, slices)
	if len(got) != 1 || got[0].IP != "10.0.0.1" {
		t.Fatalf("endpoints = %+v, want only 10.0.0.1", got)
	}
}

func TestResolveEndpointsEmptyPorts(t *testing.T) {
	// A slice with no ports (or a port list lacking the target port) yields no
	// endpoints for that target port.
	slice := endpointSlice("s1", "svc", 8080, readyEndpoint("10.0.0.1", true))
	slice.Ports = nil
	got := resolveEndpoints(8080, []*discoveryv1.EndpointSlice{slice})
	if len(got) != 0 {
		t.Fatalf("endpoints = %+v, want none", got)
	}
}

// fakeBroadcastClient records status updates and satisfies clientset.Interface.
// When indexer is set, UpdateStatus also writes the updated object back into it,
// simulating the API server persisting the status and the informer cache
// reflecting it.
type fakeBroadcastClient struct {
	updated []*v1alpha1.Broadcast
	indexer cache.Indexer
}

func (f *fakeBroadcastClient) Broadcasts(ns string) clientset.BroadcastInterface {
	return &fakeBroadcastInterface{parent: f}
}

type fakeBroadcastInterface struct {
	parent *fakeBroadcastClient
}

func (f *fakeBroadcastInterface) Get(ctx context.Context, name string, opts metav1.GetOptions) (*v1alpha1.Broadcast, error) {
	return nil, nil
}
func (f *fakeBroadcastInterface) List(ctx context.Context, opts metav1.ListOptions) (*v1alpha1.BroadcastList, error) {
	return nil, nil
}
func (f *fakeBroadcastInterface) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	return nil, nil
}
func (f *fakeBroadcastInterface) UpdateStatus(ctx context.Context, b *v1alpha1.Broadcast, opts metav1.UpdateOptions) (*v1alpha1.Broadcast, error) {
	f.parent.updated = append(f.parent.updated, b.DeepCopy())
	if f.parent.indexer != nil {
		_ = f.parent.indexer.Update(b.DeepCopy())
	}
	return b, nil
}

func newIndexer() cache.Indexer {
	return cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{
		cache.NamespaceIndex: cache.MetaNamespaceIndexFunc,
	})
}

func buildController(t *testing.T, bc *v1alpha1.Broadcast, svc *corev1.Service, slices ...*discoveryv1.EndpointSlice) (*Controller, *fakeBroadcastClient, *resolver.Memory) {
	t.Helper()

	bIdx := newIndexer()
	if bc != nil {
		if err := bIdx.Add(bc); err != nil {
			t.Fatal(err)
		}
	}
	svcIdx := newIndexer()
	if svc != nil {
		if err := svcIdx.Add(svc); err != nil {
			t.Fatal(err)
		}
	}
	epIdx := newIndexer()
	for _, s := range slices {
		if err := epIdx.Add(s); err != nil {
			t.Fatal(err)
		}
	}

	fake := &fakeBroadcastClient{indexer: bIdx}
	res := resolver.New()
	c := &Controller{
		broadcastClient:     fake,
		broadcastLister:     listers.NewBroadcastLister(bIdx),
		serviceLister:       corelisters.NewServiceLister(svcIdx),
		endpointSliceLister: discoverylisters.NewEndpointSliceLister(epIdx),
		resolver:            res,
	}
	return c, fake, res
}

func TestReconcileResolvesEndpoints(t *testing.T) {
	bc := &v1alpha1.Broadcast{
		ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "test", Generation: 1},
		Spec: v1alpha1.BroadcastSpec{
			Service: v1alpha1.BroadcastService{Name: "svc", TargetPort: 8080},
		},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "test"},
	}
	slices := []*discoveryv1.EndpointSlice{
		endpointSlice("s1", "svc", 8080,
			readyEndpoint("10.0.0.1", true),
			readyEndpoint("10.0.0.2", true),
		),
	}

	c, fake, res := buildController(t, bc, svc, slices...)

	if err := c.reconcile(context.Background(), "test/b"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	state, ok := res.State(types.NamespacedName{Namespace: "test", Name: "b"})
	if !ok {
		t.Fatal("resolver should have state for test/b")
	}
	if len(state.Endpoints) != 2 {
		t.Fatalf("endpoints = %d, want 2", len(state.Endpoints))
	}
	if state.Timeout == 0 || state.Concurrency == 0 {
		t.Fatalf("defaults not applied: %+v", state)
	}

	if len(fake.updated) != 1 {
		t.Fatalf("status updates = %d, want 1", len(fake.updated))
	}
	st := fake.updated[0].Status
	if st.Endpoints != 2 || st.ObservedGeneration != 1 {
		t.Fatalf("status = %+v", st)
	}
	if len(st.Conditions) == 0 || st.Conditions[0].Reason != reasonReady {
		t.Fatalf("conditions = %+v, want Ready", st.Conditions)
	}
}

func TestReconcileServiceNotFound(t *testing.T) {
	bc := &v1alpha1.Broadcast{
		ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "test", Generation: 1},
		Spec: v1alpha1.BroadcastSpec{
			Service: v1alpha1.BroadcastService{Name: "missing", TargetPort: 8080},
		},
	}
	c, fake, res := buildController(t, bc, nil)

	if err := c.reconcile(context.Background(), "test/b"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	state, ok := res.State(types.NamespacedName{Namespace: "test", Name: "b"})
	if !ok {
		t.Fatal("resolver should still have state (with zero endpoints)")
	}
	if len(state.Endpoints) != 0 {
		t.Fatalf("endpoints = %d, want 0", len(state.Endpoints))
	}
	if len(fake.updated) != 1 || fake.updated[0].Status.Conditions[0].Reason != reasonServiceNotFound {
		t.Fatalf("expected ServiceNotFound condition, got %+v", fake.updated)
	}
}

func TestReconcileDeletedBroadcast(t *testing.T) {
	c, _, res := buildController(t, nil, nil)
	res.Set(types.NamespacedName{Namespace: "test", Name: "b"}, resolver.State{Endpoints: []resolver.Endpoint{{IP: "10.0.0.1", Port: 8080}}})
	if err := c.reconcile(context.Background(), "test/b"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, ok := res.State(types.NamespacedName{Namespace: "test", Name: "b"}); ok {
		t.Fatal("resolver state should be deleted")
	}
}

func TestReconcileNoChangeShortCircuit(t *testing.T) {
	bc := &v1alpha1.Broadcast{
		ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "test", Generation: 1},
		Spec: v1alpha1.BroadcastSpec{
			Service: v1alpha1.BroadcastService{Name: "svc", TargetPort: 8080},
		},
	}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "test"}}
	slices := []*discoveryv1.EndpointSlice{
		endpointSlice("s1", "svc", 8080, readyEndpoint("10.0.0.1", true)),
	}
	c, fake, _ := buildController(t, bc, svc, slices...)

	if err := c.reconcile(context.Background(), "test/b"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(fake.updated) != 1 {
		t.Fatalf("first reconcile should update status once, got %d", len(fake.updated))
	}

	// Reconcile again with identical state: no status update should be issued.
	if err := c.reconcile(context.Background(), "test/b"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(fake.updated) != 1 {
		t.Fatalf("no-change reconcile should not update status, got %d updates", len(fake.updated))
	}
}

func TestReconcileEndpointCountChange(t *testing.T) {
	bc := &v1alpha1.Broadcast{
		ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "test", Generation: 1},
		Spec: v1alpha1.BroadcastSpec{
			Service: v1alpha1.BroadcastService{Name: "svc", TargetPort: 8080},
		},
	}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "test"}}

	bIdx := newIndexer()
	if err := bIdx.Add(bc); err != nil {
		t.Fatal(err)
	}
	svcIdx := newIndexer()
	if err := svcIdx.Add(svc); err != nil {
		t.Fatal(err)
	}
	epIdx := newIndexer()
	if err := epIdx.Add(endpointSlice("s1", "svc", 8080, readyEndpoint("10.0.0.1", true))); err != nil {
		t.Fatal(err)
	}

	fake := &fakeBroadcastClient{indexer: bIdx}
	res := resolver.New()
	c := &Controller{
		broadcastClient:     fake,
		broadcastLister:     listers.NewBroadcastLister(bIdx),
		serviceLister:       corelisters.NewServiceLister(svcIdx),
		endpointSliceLister: discoverylisters.NewEndpointSliceLister(epIdx),
		resolver:            res,
	}

	if err := c.reconcile(context.Background(), "test/b"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := fake.updated[len(fake.updated)-1].Status.Endpoints; got != 1 {
		t.Fatalf("endpoints = %d, want 1", got)
	}

	// A new ready endpoint appears.
	if err := epIdx.Add(endpointSlice("s2", "svc", 8080, readyEndpoint("10.0.0.2", true))); err != nil {
		t.Fatal(err)
	}
	if err := c.reconcile(context.Background(), "test/b"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	state, _ := res.State(types.NamespacedName{Namespace: "test", Name: "b"})
	if len(state.Endpoints) != 2 {
		t.Fatalf("resolver endpoints = %d, want 2", len(state.Endpoints))
	}
	if got := fake.updated[len(fake.updated)-1].Status.Endpoints; got != 2 {
		t.Fatalf("status endpoints = %d, want 2", got)
	}
}
