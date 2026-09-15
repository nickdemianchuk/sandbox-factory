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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	sandboxv1alpha1 "github.com/nickdemianchuk/sandbox-factory/api/v1alpha1"
)

var _ = Describe("Sandbox Controller", func() {
	const namespace = "default"

	ctx := context.Background()

	newReconciler := func() *SandboxReconciler {
		return &SandboxReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
	}

	It("creates an owned Pod, reflects status, and cleans up on delete", func() {
		name := "sandbox-create"
		nn := types.NamespacedName{Name: name, Namespace: namespace}
		sandbox := &sandboxv1alpha1.Sandbox{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec:       sandboxv1alpha1.SandboxSpec{Image: "busybox:1.36"},
		}
		Expect(k8sClient.Create(ctx, sandbox)).To(Succeed())

		r := newReconciler()

		By("reconciling: adds finalizer and creates the owned Pod")
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
		Expect(err).NotTo(HaveOccurred())

		By("reconciling again is idempotent (resumes the existing Pod)")
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
		Expect(err).NotTo(HaveOccurred())

		pod := &corev1.Pod{}
		Expect(k8sClient.Get(ctx, nn, pod)).To(Succeed())
		Expect(pod.Spec.Containers).To(HaveLen(1))
		Expect(pod.Spec.Containers[0].Image).To(Equal("busybox:1.36"))
		Expect(pod.OwnerReferences).To(HaveLen(1))
		Expect(pod.OwnerReferences[0].Name).To(Equal(name))

		updated := &sandboxv1alpha1.Sandbox{}
		Expect(k8sClient.Get(ctx, nn, updated)).To(Succeed())
		Expect(updated.Status.PodRef).NotTo(BeNil())
		Expect(updated.Status.PodRef.Name).To(Equal(name))
		Expect(controllerutil.ContainsFinalizer(updated, sandboxv1alpha1.SandboxFinalizer)).To(BeTrue())

		By("deleting the sandbox")
		Expect(k8sClient.Delete(ctx, updated)).To(Succeed())
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() bool {
			return errors.IsNotFound(k8sClient.Get(ctx, nn, &sandboxv1alpha1.Sandbox{}))
		}).Should(BeTrue())
	})

	It("expires the sandbox once its TTL elapses, retaining the CR", func() {
		name := "sandbox-ttl"
		nn := types.NamespacedName{Name: name, Namespace: namespace}
		ttl := int32(1)
		sandbox := &sandboxv1alpha1.Sandbox{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: sandboxv1alpha1.SandboxSpec{
				Image:                   "busybox:1.36",
				TTLSecondsAfterCreation: &ttl,
			},
		}
		Expect(k8sClient.Create(ctx, sandbox)).To(Succeed())

		r := newReconciler()

		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
		Expect(err).NotTo(HaveOccurred())
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
		Expect(err).NotTo(HaveOccurred())

		pod := &corev1.Pod{}
		Expect(k8sClient.Get(ctx, nn, pod)).To(Succeed())

		time.Sleep(1500 * time.Millisecond)

		By("reconciling after the TTL has elapsed")
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
		Expect(err).NotTo(HaveOccurred())

		updated := &sandboxv1alpha1.Sandbox{}
		Expect(k8sClient.Get(ctx, nn, updated)).To(Succeed())
		Expect(updated.Status.Phase).To(Equal(sandboxv1alpha1.SandboxPhaseExpired))

		Eventually(func() bool {
			return errors.IsNotFound(k8sClient.Get(ctx, nn, &corev1.Pod{}))
		}).Should(BeTrue())

		By("cleaning up the retained CR")
		Expect(k8sClient.Delete(ctx, updated)).To(Succeed())
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
		Expect(err).NotTo(HaveOccurred())
	})
})
