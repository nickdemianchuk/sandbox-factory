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

package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	sandboxv1alpha1 "github.com/nickdemianchuk/sandbox-factory/api/v1alpha1"
)

// sandboxLabel identifies the Sandbox a Pod/Service/NetworkPolicy belongs to.
const sandboxLabel = "sandbox.sandbox-factory.io/sandbox"

// SandboxReconciler reconciles a Sandbox object
type SandboxReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=sandbox.sandbox-factory.io,resources=sandboxes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sandbox.sandbox-factory.io,resources=sandboxes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=sandbox.sandbox-factory.io,resources=sandboxes/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile drives a Sandbox towards its desired state: it creates the owned
// Pod (and, if requested, Service/NetworkPolicy), mirrors Pod status back
// onto the Sandbox, and expires the sandbox once its TTL elapses.
func (r *SandboxReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	sandbox := &sandboxv1alpha1.Sandbox{}
	if err := r.Get(ctx, req.NamespacedName, sandbox); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !sandbox.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, sandbox)
	}

	if !controllerutil.ContainsFinalizer(sandbox, sandboxv1alpha1.SandboxFinalizer) {
		controllerutil.AddFinalizer(sandbox, sandboxv1alpha1.SandboxFinalizer)
		if err := r.Update(ctx, sandbox); err != nil {
			return ctrl.Result{}, err
		}
		// sandbox is now updated in-memory too; fall through and finish
		// reconciling in this same pass instead of requeuing.
	}

	// Once expired, the sandbox is done: its children are gone and the CR is
	// retained for audit purposes only. Don't recreate anything.
	if sandbox.Status.Phase == sandboxv1alpha1.SandboxPhaseExpired {
		return ctrl.Result{}, nil
	}

	if expiry := computeExpiryTime(sandbox); expiry != nil && !time.Now().Before(*expiry) {
		return r.expire(ctx, sandbox)
	}

	if sandbox.Spec.Image == "" {
		apimeta.SetStatusCondition(&sandbox.Status.Conditions, metav1.Condition{
			Type:               sandboxv1alpha1.SandboxConditionReady,
			Status:             metav1.ConditionFalse,
			Reason:             "InvalidSpec",
			Message:            "spec.image is required",
			ObservedGeneration: sandbox.Generation,
		})
		sandbox.Status.Phase = sandboxv1alpha1.SandboxPhaseFailed
		return ctrl.Result{}, r.Status().Update(ctx, sandbox)
	}

	pod, err := r.reconcilePod(ctx, sandbox)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling pod: %w", err)
	}

	var svc *corev1.Service
	if len(sandbox.Spec.Ports) > 0 {
		svc, err = r.reconcileService(ctx, sandbox)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("reconciling service: %w", err)
		}
	}

	if sandbox.Spec.NetworkPolicy != nil {
		if err := r.reconcileNetworkPolicy(ctx, sandbox); err != nil {
			return ctrl.Result{}, fmt.Errorf("reconciling network policy: %w", err)
		}
	}

	r.syncStatus(sandbox, pod, svc)
	sandbox.Status.ObservedGeneration = sandbox.Generation

	expiry := computeExpiryTime(sandbox)
	sandbox.Status.ExpiryTime = timeToMetaTime(expiry)

	if err := r.Status().Update(ctx, sandbox); err != nil {
		return ctrl.Result{}, err
	}

	if expiry != nil {
		log.Info("requeueing for TTL expiry", "expiryTime", expiry)
		return ctrl.Result{RequeueAfter: time.Until(*expiry)}, nil
	}
	return ctrl.Result{}, nil
}

// finalize removes owned resources (cascade-deleted via owner references by
// the garbage collector) and clears the finalizer so deletion can proceed.
func (r *SandboxReconciler) finalize(ctx context.Context, sandbox *sandboxv1alpha1.Sandbox) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(sandbox, sandboxv1alpha1.SandboxFinalizer) {
		return ctrl.Result{}, nil
	}
	controllerutil.RemoveFinalizer(sandbox, sandboxv1alpha1.SandboxFinalizer)
	if err := r.Update(ctx, sandbox); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// expire deletes the sandbox's owned Pod/Service/NetworkPolicy and marks the
// Sandbox Expired. The Sandbox object itself is retained for audit purposes.
func (r *SandboxReconciler) expire(ctx context.Context, sandbox *sandboxv1alpha1.Sandbox) (ctrl.Result, error) {
	key := client.ObjectKeyFromObject(sandbox)

	pod := &corev1.Pod{}
	if err := r.Get(ctx, key, pod); err == nil {
		if err := r.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	} else if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	svc := &corev1.Service{}
	if err := r.Get(ctx, key, svc); err == nil {
		if err := r.Delete(ctx, svc); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	} else if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	netpol := &networkingv1.NetworkPolicy{}
	if err := r.Get(ctx, key, netpol); err == nil {
		if err := r.Delete(ctx, netpol); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	} else if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	sandbox.Status.Phase = sandboxv1alpha1.SandboxPhaseExpired
	sandbox.Status.PodRef = nil
	sandbox.Status.ServiceRef = nil
	sandbox.Status.Endpoint = ""
	sandbox.Status.ObservedGeneration = sandbox.Generation
	apimeta.SetStatusCondition(&sandbox.Status.Conditions, metav1.Condition{
		Type:               sandboxv1alpha1.SandboxConditionReady,
		Status:             metav1.ConditionFalse,
		Reason:             "Expired",
		Message:            "sandbox TTL elapsed",
		ObservedGeneration: sandbox.Generation,
	})

	return ctrl.Result{}, r.Status().Update(ctx, sandbox)
}

// reconcilePod resumes the sandbox's Pod if it already exists (e.g. after a
// controller restart), otherwise creates it. Pod name == Sandbox name, so
// there's a 1:1 mapping and no owner-ref list scan is needed.
func (r *SandboxReconciler) reconcilePod(ctx context.Context, sandbox *sandboxv1alpha1.Sandbox) (*corev1.Pod, error) {
	pod := &corev1.Pod{}
	err := r.Get(ctx, client.ObjectKeyFromObject(sandbox), pod)
	if err == nil {
		return pod, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}

	pod = buildPod(sandbox)
	if err := controllerutil.SetControllerReference(sandbox, pod, r.Scheme); err != nil {
		return nil, err
	}
	if err := r.Create(ctx, pod); err != nil {
		return nil, err
	}
	return pod, nil
}

func (r *SandboxReconciler) reconcileService(ctx context.Context, sandbox *sandboxv1alpha1.Sandbox) (*corev1.Service, error) {
	svc := &corev1.Service{}
	err := r.Get(ctx, client.ObjectKeyFromObject(sandbox), svc)
	if err == nil {
		return svc, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}

	svc = buildService(sandbox)
	if err := controllerutil.SetControllerReference(sandbox, svc, r.Scheme); err != nil {
		return nil, err
	}
	if err := r.Create(ctx, svc); err != nil {
		return nil, err
	}
	return svc, nil
}

func (r *SandboxReconciler) reconcileNetworkPolicy(ctx context.Context, sandbox *sandboxv1alpha1.Sandbox) error {
	netpol := &networkingv1.NetworkPolicy{}
	err := r.Get(ctx, client.ObjectKeyFromObject(sandbox), netpol)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}

	netpol = buildNetworkPolicy(sandbox)
	if err := controllerutil.SetControllerReference(sandbox, netpol, r.Scheme); err != nil {
		return err
	}
	return r.Create(ctx, netpol)
}

// syncStatus mirrors observed Pod/Service state onto the Sandbox status.
func (r *SandboxReconciler) syncStatus(sandbox *sandboxv1alpha1.Sandbox, pod *corev1.Pod, svc *corev1.Service) {
	sandbox.Status.Phase = podPhaseToSandboxPhase(pod.Status.Phase)
	sandbox.Status.PodRef = &corev1.LocalObjectReference{Name: pod.Name}
	if sandbox.Status.StartTime == nil {
		now := metav1.Now()
		sandbox.Status.StartTime = &now
	}

	podScheduled := metav1.ConditionFalse
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionTrue {
			podScheduled = metav1.ConditionTrue
			break
		}
	}
	apimeta.SetStatusCondition(&sandbox.Status.Conditions, metav1.Condition{
		Type:               sandboxv1alpha1.SandboxConditionPodScheduled,
		Status:             podScheduled,
		Reason:             "PodStatusObserved",
		ObservedGeneration: sandbox.Generation,
	})

	ready := metav1.ConditionFalse
	reason := "PodNotRunning"
	if pod.Status.Phase == corev1.PodRunning {
		ready = metav1.ConditionTrue
		reason = "PodRunning"
	}
	apimeta.SetStatusCondition(&sandbox.Status.Conditions, metav1.Condition{
		Type:               sandboxv1alpha1.SandboxConditionReady,
		Status:             ready,
		Reason:             reason,
		ObservedGeneration: sandbox.Generation,
	})

	if svc != nil {
		sandbox.Status.ServiceRef = &corev1.LocalObjectReference{Name: svc.Name}
		sandbox.Status.Endpoint = fmt.Sprintf("%s.%s.svc.cluster.local", svc.Name, svc.Namespace)
	}
}

func podPhaseToSandboxPhase(phase corev1.PodPhase) sandboxv1alpha1.SandboxPhase {
	switch phase {
	case corev1.PodRunning:
		return sandboxv1alpha1.SandboxPhaseRunning
	case corev1.PodFailed, corev1.PodSucceeded:
		return sandboxv1alpha1.SandboxPhaseFailed
	case corev1.PodPending:
		return sandboxv1alpha1.SandboxPhaseProvisioning
	default:
		return sandboxv1alpha1.SandboxPhasePending
	}
}

// computeExpiryTime returns the absolute time the sandbox's TTL elapses, or
// nil if no TTL is set. Idle timeout is reserved but not yet factored in.
func computeExpiryTime(sandbox *sandboxv1alpha1.Sandbox) *time.Time {
	if sandbox.Spec.TTLSecondsAfterCreation == nil {
		return nil
	}
	expiry := sandbox.CreationTimestamp.Add(time.Duration(*sandbox.Spec.TTLSecondsAfterCreation) * time.Second)
	return &expiry
}

func timeToMetaTime(t *time.Time) *metav1.Time {
	if t == nil {
		return nil
	}
	mt := metav1.NewTime(*t)
	return &mt
}

func sandboxLabels(sandbox *sandboxv1alpha1.Sandbox) map[string]string {
	return map[string]string{sandboxLabel: sandbox.Name}
}

func buildPod(sandbox *sandboxv1alpha1.Sandbox) *corev1.Pod {
	ports := make([]corev1.ContainerPort, 0, len(sandbox.Spec.Ports))
	for _, p := range sandbox.Spec.Ports {
		ports = append(ports, corev1.ContainerPort{
			Name:          p.Name,
			ContainerPort: p.ContainerPort,
			Protocol:      p.Protocol,
		})
	}

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sandbox.Name,
			Namespace: sandbox.Namespace,
			Labels:    sandboxLabels(sandbox),
		},
		Spec: corev1.PodSpec{
			// Sandboxes are on-demand, single-run environments: a container
			// exit is a terminal outcome the controller should observe as
			// Failed, not something kubelet should retry.
			RestartPolicy:      corev1.RestartPolicyNever,
			ServiceAccountName: sandbox.Spec.ServiceAccountName,
			SecurityContext:    sandbox.Spec.PodSecurityContext,
			RuntimeClassName:   sandbox.Spec.RuntimeClassName,
			NodeSelector:       sandbox.Spec.NodeSelector,
			Tolerations:        sandbox.Spec.Tolerations,
			Volumes:            sandbox.Spec.Volumes,
			Containers: []corev1.Container{
				{
					Name:            "sandbox",
					Image:           sandbox.Spec.Image,
					Command:         sandbox.Spec.Command,
					Args:            sandbox.Spec.Args,
					Env:             sandbox.Spec.Env,
					Resources:       sandbox.Spec.Resources,
					SecurityContext: sandbox.Spec.SecurityContext,
					Ports:           ports,
					VolumeMounts:    sandbox.Spec.VolumeMounts,
				},
			},
		},
	}
}

func buildService(sandbox *sandboxv1alpha1.Sandbox) *corev1.Service {
	ports := make([]corev1.ServicePort, 0, len(sandbox.Spec.Ports))
	for _, p := range sandbox.Spec.Ports {
		if !p.Expose {
			continue
		}
		ports = append(ports, corev1.ServicePort{
			Name:       p.Name,
			Port:       p.ContainerPort,
			TargetPort: intstr.FromInt32(p.ContainerPort),
			Protocol:   p.Protocol,
		})
	}

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sandbox.Name,
			Namespace: sandbox.Namespace,
			Labels:    sandboxLabels(sandbox),
		},
		Spec: corev1.ServiceSpec{
			Selector: sandboxLabels(sandbox),
			Ports:    ports,
			Type:     corev1.ServiceTypeClusterIP,
		},
	}
}

func buildNetworkPolicy(sandbox *sandboxv1alpha1.Sandbox) *networkingv1.NetworkPolicy {
	spec := sandbox.Spec.NetworkPolicy

	var policyTypes []networkingv1.PolicyType
	var egressRules []networkingv1.NetworkPolicyEgressRule

	if spec.DenyAll || len(spec.EgressAllowed) > 0 {
		policyTypes = append(policyTypes, networkingv1.PolicyTypeEgress)
		for _, cidr := range spec.EgressAllowed {
			egressRules = append(egressRules, networkingv1.NetworkPolicyEgressRule{
				To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: cidr}}},
			})
		}
	}
	if spec.DenyAll {
		policyTypes = append(policyTypes, networkingv1.PolicyTypeIngress)
	}

	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sandbox.Name,
			Namespace: sandbox.Namespace,
			Labels:    sandboxLabels(sandbox),
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: sandboxLabels(sandbox)},
			PolicyTypes: policyTypes,
			Egress:      egressRules,
		},
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *SandboxReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sandboxv1alpha1.Sandbox{}).
		Owns(&corev1.Pod{}).
		Owns(&corev1.Service{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Named("sandbox").
		Complete(r)
}
