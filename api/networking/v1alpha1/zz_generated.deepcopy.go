package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// DeepCopyInto copies the receiver into out.
func (in *BroadcastService) DeepCopyInto(out *BroadcastService) {
	*out = *in
}

// DeepCopy returns a deep copy of the receiver.
func (in *BroadcastService) DeepCopy() *BroadcastService {
	if in == nil {
		return nil
	}
	out := new(BroadcastService)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto copies the receiver into out.
func (in *BroadcastSpec) DeepCopyInto(out *BroadcastSpec) {
	*out = *in
	if in.Timeout != nil {
		in, out := &in.Timeout, &out.Timeout
		*out = new(metav1.Duration)
		**out = **in
	}
	if in.Concurrency != nil {
		in, out := &in.Concurrency, &out.Concurrency
		*out = new(int32)
		**out = **in
	}
}

// DeepCopy returns a deep copy of the receiver.
func (in *BroadcastSpec) DeepCopy() *BroadcastSpec {
	if in == nil {
		return nil
	}
	out := new(BroadcastSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto copies the receiver into out.
func (in *BroadcastStatus) DeepCopyInto(out *BroadcastStatus) {
	*out = *in
	if in.Conditions != nil {
		in, out := &in.Conditions, &out.Conditions
		*out = make([]metav1.Condition, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy returns a deep copy of the receiver.
func (in *BroadcastStatus) DeepCopy() *BroadcastStatus {
	if in == nil {
		return nil
	}
	out := new(BroadcastStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto copies the receiver into out.
func (in *Broadcast) DeepCopyInto(out *Broadcast) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

// DeepCopy returns a deep copy of the receiver.
func (in *Broadcast) DeepCopy() *Broadcast {
	if in == nil {
		return nil
	}
	out := new(Broadcast)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject implements runtime.Object.
func (in *Broadcast) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto copies the receiver into out.
func (in *BroadcastList) DeepCopyInto(out *BroadcastList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]Broadcast, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy returns a deep copy of the receiver.
func (in *BroadcastList) DeepCopy() *BroadcastList {
	if in == nil {
		return nil
	}
	out := new(BroadcastList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject implements runtime.Object.
func (in *BroadcastList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}
