// Package listers provides read-only, cache-backed access to Broadcast objects.
package listers

import (
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"

	"github.com/goxang/broadcast/api/networking/v1alpha1"
)

// BroadcastLister lists Broadcasts from an informer's cache.
type BroadcastLister interface {
	List(selector labels.Selector) ([]*v1alpha1.Broadcast, error)
	Broadcasts(namespace string) BroadcastNamespaceLister
}

// BroadcastNamespaceLister lists Broadcasts in a single namespace.
type BroadcastNamespaceLister interface {
	List(selector labels.Selector) ([]*v1alpha1.Broadcast, error)
	Get(name string) (*v1alpha1.Broadcast, error)
}

type broadcastLister struct {
	indexer cache.Indexer
}

// NewBroadcastLister returns a lister backed by indexer.
func NewBroadcastLister(indexer cache.Indexer) BroadcastLister {
	return &broadcastLister{indexer: indexer}
}

func (l *broadcastLister) List(selector labels.Selector) ([]*v1alpha1.Broadcast, error) {
	var out []*v1alpha1.Broadcast
	err := cache.ListAll(l.indexer, selector, func(m interface{}) {
		if b, ok := m.(*v1alpha1.Broadcast); ok {
			out = append(out, b)
		}
	})
	return out, err
}

func (l *broadcastLister) Broadcasts(namespace string) BroadcastNamespaceLister {
	return &broadcastNamespaceLister{indexer: l.indexer, namespace: namespace}
}

type broadcastNamespaceLister struct {
	indexer   cache.Indexer
	namespace string
}

func (l *broadcastNamespaceLister) List(selector labels.Selector) ([]*v1alpha1.Broadcast, error) {
	var out []*v1alpha1.Broadcast
	err := cache.ListAllByNamespace(l.indexer, l.namespace, selector, func(m interface{}) {
		if b, ok := m.(*v1alpha1.Broadcast); ok {
			out = append(out, b)
		}
	})
	return out, err
}

func (l *broadcastNamespaceLister) Get(name string) (*v1alpha1.Broadcast, error) {
	obj, exists, err := l.indexer.GetByKey(l.namespace + "/" + name)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	b, ok := obj.(*v1alpha1.Broadcast)
	if !ok {
		return nil, nil
	}
	return b, nil
}
