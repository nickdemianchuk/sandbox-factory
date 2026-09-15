/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// SandboxPhase is a high-level summary of where the Sandbox is in its lifecycle.
type SandboxPhase string

const (
	SandboxPhasePending      SandboxPhase = "Pending"
	SandboxPhaseProvisioning SandboxPhase = "Provisioning"
	SandboxPhaseRunning      SandboxPhase = "Running"
	SandboxPhaseTerminating  SandboxPhase = "Terminating"
	SandboxPhaseFailed       SandboxPhase = "Failed"
	SandboxPhaseExpired      SandboxPhase = "Expired"
)

// Condition types set on a Sandbox.
const (
	SandboxConditionReady        = "Ready"
	SandboxConditionPodScheduled = "PodScheduled"
)

// SandboxFinalizer is added to a Sandbox so the controller can clean up owned
// resources and any external state before the object is removed.
const SandboxFinalizer = "sandbox.sandbox-factory.io/finalizer"

// SandboxPort declares a container port that should be reachable via the
// Sandbox's Service.
type SandboxPort struct {
	// name identifies this port within the Sandbox.
	// +required
	Name string `json:"name"`

	// containerPort is the port the sandbox container listens on.
	// +required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	ContainerPort int32 `json:"containerPort"`

	// protocol is the IP protocol for this port. Defaults to TCP.
	// +optional
	// +kubebuilder:default=TCP
	Protocol corev1.Protocol `json:"protocol,omitempty"`

	// expose controls whether this port is included on the Sandbox's Service.
	// +optional
	// +kubebuilder:default=true
	Expose bool `json:"expose,omitempty"`
}

// SandboxNetworkPolicy configures the NetworkPolicy generated for a Sandbox.
type SandboxNetworkPolicy struct {
	// denyAll, when true, blocks all ingress and egress traffic except what is
	// explicitly allowed by egressAllowed.
	// +optional
	DenyAll bool `json:"denyAll,omitempty"`

	// egressAllowed lists CIDR blocks the sandbox is permitted to reach when
	// denyAll is set.
	// +optional
	EgressAllowed []string `json:"egressAllowed,omitempty"`
}

// SandboxSpec defines the desired state of Sandbox.
type SandboxSpec struct {
	// image is the container image to run in the sandbox.
	// +required
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// command overrides the container entrypoint.
	// +optional
	Command []string `json:"command,omitempty"`

	// args overrides the container arguments.
	// +optional
	Args []string `json:"args,omitempty"`

	// env sets environment variables in the sandbox container.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// resources describes the compute resource requirements for the sandbox
	// container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// securityContext is applied to the sandbox container.
	// +optional
	SecurityContext *corev1.SecurityContext `json:"securityContext,omitempty"`

	// podSecurityContext is applied to the sandbox Pod.
	// +optional
	PodSecurityContext *corev1.PodSecurityContext `json:"podSecurityContext,omitempty"`

	// runtimeClassName selects the container runtime used to isolate the
	// sandbox (e.g. gVisor, Kata, Firecracker).
	// +optional
	RuntimeClassName *string `json:"runtimeClassName,omitempty"`

	// serviceAccountName is the service account the sandbox Pod runs as.
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// volumes are made available to the sandbox container via volumeMounts.
	// +optional
	Volumes []corev1.Volume `json:"volumes,omitempty"`

	// volumeMounts mounts volumes into the sandbox container.
	// +optional
	VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty"`

	// nodeSelector constrains the sandbox Pod to nodes with matching labels.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// tolerations allow the sandbox Pod to schedule onto tainted nodes.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// ttlSecondsAfterCreation is the hard lifetime of the sandbox. Once
	// elapsed, the controller deletes the owned Pod/Service and sets the
	// Sandbox phase to Expired; the Sandbox object itself is retained.
	// +optional
	// +kubebuilder:validation:Minimum=1
	TTLSecondsAfterCreation *int32 `json:"ttlSecondsAfterCreation,omitempty"`

	// idleTimeoutSeconds is reserved for a future activity-based expiry
	// mechanism. It is validated but currently has no effect.
	// +optional
	// +kubebuilder:validation:Minimum=1
	IdleTimeoutSeconds *int32 `json:"idleTimeoutSeconds,omitempty"`

	// ports declares container ports that should be reachable via a Service.
	// +optional
	Ports []SandboxPort `json:"ports,omitempty"`

	// networkPolicy configures network isolation for the sandbox.
	// +optional
	NetworkPolicy *SandboxNetworkPolicy `json:"networkPolicy,omitempty"`
}

// SandboxStatus defines the observed state of Sandbox.
type SandboxStatus struct {
	// phase is a high-level summary of where the Sandbox is in its lifecycle.
	// +optional
	Phase SandboxPhase `json:"phase,omitempty"`

	// podRef references the Pod backing this sandbox.
	// +optional
	PodRef *corev1.LocalObjectReference `json:"podRef,omitempty"`

	// serviceRef references the Service exposing this sandbox, if any.
	// +optional
	ServiceRef *corev1.LocalObjectReference `json:"serviceRef,omitempty"`

	// endpoint is the address other workloads can use to reach the sandbox.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// startTime is when the sandbox Pod was created.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// lastActivityTime is reserved for the future idle-timeout mechanism.
	// +optional
	LastActivityTime *metav1.Time `json:"lastActivityTime,omitempty"`

	// expiryTime is the computed absolute time at which the sandbox will be
	// terminated, derived from ttlSecondsAfterCreation.
	// +optional
	ExpiryTime *metav1.Time `json:"expiryTime,omitempty"`

	// conditions represent the current state of the Sandbox resource.
	//
	// Standard condition types:
	// - "Ready": the sandbox Pod is running and reachable
	// - "PodScheduled": the sandbox Pod has been created and scheduled
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// observedGeneration is the most recent generation observed by the
	// controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.image`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Sandbox is the Schema for the sandboxes API
type Sandbox struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Sandbox
	// +required
	Spec SandboxSpec `json:"spec"`

	// status defines the observed state of Sandbox
	// +optional
	Status SandboxStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// SandboxList contains a list of Sandbox
type SandboxList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Sandbox `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Sandbox{}, &SandboxList{})
		return nil
	})
}
