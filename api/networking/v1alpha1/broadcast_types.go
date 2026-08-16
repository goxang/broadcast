package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BroadcastProtocol is the application protocol used to fan requests out to
// target endpoints.
type BroadcastProtocol string

const (
	// ProtocolHTTP fans requests out using HTTP/1.1. This is the only protocol
	// supported in v1alpha1.
	ProtocolHTTP BroadcastProtocol = "HTTP"
)

// Default values applied by the CRD schema. They are mirrored here so callers
// (the controller and proxy) have a single source of truth in code.
const (
	// DefaultProtocol is the protocol used when spec.protocol is unset.
	DefaultProtocol = ProtocolHTTP
	// DefaultConcurrency bounds the number of concurrent target requests per
	// broadcast when spec.concurrency is unset.
	DefaultConcurrency int32 = 16
)

// DefaultTimeout is the fan-out timeout used when spec.timeout is unset.
var DefaultTimeout = metav1.Duration{Duration: 1000000000} // 1s

// BroadcastService identifies an existing Kubernetes Service in the same
// namespace whose ready endpoints are the broadcast targets.
type BroadcastService struct {
	// Name is the name of the Service in the Broadcast's namespace.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// TargetPort is the port number the endpoints listen on. It is matched
	// against the EndpointSlice endpoint port (the pod port), exactly like a
	// Service's targetPort. It is NOT the Service port, so it is unambiguous
	// even when the Service maps a port to a different targetPort.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	TargetPort int32 `json:"targetPort"`
}

// BroadcastSpec defines the desired state of a Broadcast.
type BroadcastSpec struct {
	// Service is the existing Service whose ready endpoints are fanned out to.
	Service BroadcastService `json:"service"`

	// Protocol is the fan-out protocol. Only "HTTP" is supported in v1alpha1.
	// +kubebuilder:validation:Enum=HTTP
	// +kubebuilder:default=HTTP
	Protocol BroadcastProtocol `json:"protocol,omitempty"`

	// Timeout bounds the entire fan-out operation. Individual target requests
	// share this budget; the proxy never waits longer than this for target
	// responses. Defaults to "1s".
	// +kubebuilder:default="1s"
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// Concurrency is the maximum number of target requests in flight at once
	// for a single broadcast. It bounds the goroutine/connection fan-out for a
	// large target set. Defaults to 16.
	// +kubebuilder:default=16
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1024
	Concurrency *int32 `json:"concurrency,omitempty"`
}

// BroadcastConditionType is a condition type carried on a Broadcast's status.
type BroadcastConditionType string

const (
	// BroadcastReady indicates whether the Broadcast is able to fan out to at
	// least one ready endpoint. When false, the proxy returns 503 for requests
	// targeting this Broadcast.
	BroadcastReady BroadcastConditionType = "Ready"
)

// BroadcastStatus defines the observed state of a Broadcast.
type BroadcastStatus struct {
	// Endpoints is the number of ready endpoints the controller currently
	// resolves for this Broadcast.
	Endpoints int32 `json:"endpoints,omitempty"`

	// ObservedGeneration is the most recent generation observed by the
	// controller, used to detect spec changes that have not been reconciled.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions reports the current reconciliation state. The Ready condition
	// is True when the referenced Service resolves to at least one ready
	// endpoint.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Broadcast fans a single HTTP request out to every ready endpoint of a
// Service. It is a best-effort primitive: it provides no acknowledgement,
// retry, ordering, or delivery guarantee.
type Broadcast struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BroadcastSpec   `json:"spec,omitempty"`
	Status BroadcastStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// BroadcastList is a list of Broadcast objects.
type BroadcastList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Broadcast `json:"items"`
}
