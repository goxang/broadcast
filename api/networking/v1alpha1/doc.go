// Package v1alpha1 contains the API types for the Broadcast primitive.
//
// Broadcast fans a single HTTP request out to every ready endpoint backing a
// Kubernetes Service. Delivery is best-effort: there is no acknowledgement,
// retry, ordering, or delivery guarantee.
//
// +groupName=networking.goxang.io
package v1alpha1
