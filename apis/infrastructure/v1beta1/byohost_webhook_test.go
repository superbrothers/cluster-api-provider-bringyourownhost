// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	byohv1beta1 "github.com/superbrothers/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	v1beta1patch "sigs.k8s.io/cluster-api/util/deprecated/v1beta1/patch"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func newByoHost(name string) *byohv1beta1.ByoHost {
	return &byohv1beta1.ByoHost{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ByoHost",
			APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: byohv1beta1.ByoHostSpec{},
	}
}

// deleteByoHost deletes a ByoHost as an administrator, after detaching it from
// its ByoMachine.
func deleteByoHost(byoHost *byohv1beta1.ByoHost) {
	current := &byohv1beta1.ByoHost{}
	err := k8sClient.Get(ctx, client.ObjectKeyFromObject(byoHost), current)
	if apierrors.IsNotFound(err) {
		return
	}
	Expect(err).NotTo(HaveOccurred())
	if current.Spec.ByoMachineRef != nil {
		ph, err := v1beta1patch.NewHelper(current, k8sClient)
		Expect(err).NotTo(HaveOccurred())
		current.Spec = byohv1beta1.ByoHostSpec{}
		Expect(ph.Patch(ctx, current)).To(Succeed())
	}
	Expect(k8sClient.Delete(ctx, current)).To(Succeed())
}

// attach attaches a ByoHost to the ByoMachine byomachine1 as an
// administrator, in the way the controller does.
func attach(byoHost *byohv1beta1.ByoHost) {
	ph, err := v1beta1patch.NewHelper(byoHost, k8sClient)
	Expect(err).NotTo(HaveOccurred())
	byoHost.Spec.ByoMachineRef = &byohv1beta1.ByoMachineReference{Name: "byomachine1", UID: "uid-of-byomachine1"}
	byoHost.Spec.BootstrapSecret = &corev1.ObjectReference{Kind: "Secret", Namespace: "default", Name: "byomachine1"}
	Expect(ph.Patch(ctx, byoHost)).To(Succeed())
}

// grantToHost3 lets the agent of host3 use the verbs on ByoHosts with RBAC,
// beyond the RBAC of the agents, so that a test checks the webhook alone.
func grantToHost3(verbs ...string) {
	role := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "test-byohost-host3-"},
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{"infrastructure.cluster.x-k8s.io"},
			Resources: []string{"byohosts"},
			Verbs:     verbs,
		}},
	}
	Expect(k8sClient.Create(ctx, role)).To(Succeed())
	DeferCleanup(func() { Expect(k8sClient.Delete(ctx, role)).To(Succeed()) })
	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "test-byohost-host3-"},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: role.Name},
		Subjects:   []rbacv1.Subject{{APIGroup: "rbac.authorization.k8s.io", Kind: "User", Name: "byoh:host:host3"}},
	}
	Expect(k8sClient.Create(ctx, binding)).To(Succeed())
	DeferCleanup(func() { Expect(k8sClient.Delete(ctx, binding)).To(Succeed()) })
}

// denied returns the error message of the webhook that denies a request.
func denied(message string) string {
	return `admission webhook "vbyohost.kb.io" denied the request: ` + message
}

var _ = Describe("ByohostWebhook", func() {
	BeforeEach(func() {
		ctx = context.Background()
	})

	Context("When ByoHost gets a delete request", func() {
		var byoHost *byohv1beta1.ByoHost
		BeforeEach(func() {
			byoHost = newByoHost("host1")
			Expect(ValidUserK8sClient.Create(ctx, byoHost)).Should(Succeed())
		})
		AfterEach(func() {
			deleteByoHost(byoHost)
		})

		It("should not let the agent of the host delete it with RBAC", func() {
			err := ValidUserK8sClient.Delete(ctx, byoHost)
			Expect(apierrors.IsForbidden(err)).To(BeTrue(), "%v", err)
			Expect(err.Error()).To(ContainSubstring(`User "byoh:host:host1" cannot delete resource "byohosts"`))
		})

		It("should reject the request from an agent in the webhook even when RBAC allows it", func() {
			host3 := newByoHost("host3")
			Expect(k8sClient.Create(ctx, host3)).To(Succeed())
			DeferCleanup(deleteByoHost, host3)
			grantToHost3("delete")

			Eventually(func() string {
				err := Host3K8sClient.Delete(ctx, host3)
				if err == nil {
					return ""
				}
				return err.Error()
			}).Should(Equal(denied("byoh:host:host3 cannot delete ByoHost host3: a member of byoh:hosts cannot delete ByoHosts")))
		})

		It("should allow the request from an administrator", func() {
			Expect(k8sClient.Delete(ctx, byoHost)).To(Succeed())
		})

		It("should reject the request when the ByoHost is attached to a ByoMachine", func() {
			attach(byoHost)
			err := k8sClient.Delete(ctx, byoHost)
			Expect(err).To(MatchError(denied("cannot delete ByoHost host1 while it is attached to ByoMachine byomachine1 (spec.byoMachineRef)")))
		})
	})

	Context("When ByoHost gets a create request", func() {
		var byoHost *byohv1beta1.ByoHost
		BeforeEach(func() {
			byoHost = newByoHost("host1")
		})
		AfterEach(func() {
			deleteByoHost(byoHost)
		})

		It("should allow the agent to create its ByoHost with the labels of --label", func() {
			byoHost.Labels = map[string]string{"role": "worker"}
			Expect(ValidUserK8sClient.Create(ctx, byoHost)).Should(Succeed())
		})

		It("should reject the request from an invalid user", func() {
			err := InvalidUserK8sClient.Create(ctx, byoHost)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("test-user cannot create ByoHost host1"))
		})

		It("should reject the request from the agent of another host whose name is contained", func() {
			byoHost.Name = "host10"
			err := ValidUserK8sClient.Create(ctx, byoHost)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("byoh:host:host1 cannot create ByoHost host10"))
		})

		It("should reject a ByoHost with a label of Cluster API", func() {
			byoHost.Labels = map[string]string{"cluster.x-k8s.io/cluster-name": "cluster1"}
			err := ValidUserK8sClient.Create(ctx, byoHost)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("byoh:host:host1 cannot create ByoHost host1: metadata.labels[cluster.x-k8s.io/cluster-name]"))
		})

		It("should reject a ByoHost that is attached to a ByoMachine", func() {
			byoHost.Spec.ByoMachineRef = &byohv1beta1.ByoMachineReference{Name: "byomachine1", UID: "uid-of-byomachine1"}
			err := ValidUserK8sClient.Create(ctx, byoHost)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("byoh:host:host1 cannot create ByoHost host1: spec: Forbidden: only the controller sets the spec"))
		})

		// This pins a known limitation: the identity of a host does not
		// include a namespace (docs/host_registration.md, "Current
		// limitations"), so the agent of a host can create a ByoHost of its
		// name in any namespace, with labels of its choice. When the identity
		// includes the namespace, this request must be rejected.
		It("should allow the agent to create a ByoHost of its name in another namespace", func() {
			namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "byohost-webhook-other"}}
			if err := k8sClient.Create(ctx, namespace); err != nil {
				Expect(apierrors.IsAlreadyExists(err)).To(BeTrue(), "%v", err)
			}
			Expect(ValidUserK8sClient.Create(ctx, newByoHost("host1"))).To(Succeed())
			DeferCleanup(deleteByoHost, newByoHost("host1"))

			byoHost.Namespace = namespace.Name
			byoHost.Labels = map[string]string{"role": "control-plane"}
			Expect(ValidUserK8sClient.Create(ctx, byoHost)).To(Succeed())
		})

		It("should allow the request from an administrator", func() {
			Expect(k8sClient.Create(ctx, byoHost)).Should(Succeed())
		})
	})

	Context("When ByoHost gets an update request", func() {
		var byoHost *byohv1beta1.ByoHost
		BeforeEach(func() {
			byoHost = newByoHost("host1")
			byoHost.Labels = map[string]string{"role": "worker"}
			Expect(ValidUserK8sClient.Create(ctx, byoHost)).Should(Succeed())
		})
		AfterEach(func() {
			deleteByoHost(byoHost)
		})

		It("should allow the agent of the host to report its status", func() {
			ph, err := v1beta1patch.NewHelper(byoHost, ValidUserK8sClient)
			Expect(err).NotTo(HaveOccurred())
			byoHost.Status.HostDetails.Architecture = "arm64"
			byoHost.Status.Network = []byohv1beta1.NetworkStatus{{NetworkInterfaceName: "eth0", IsDefault: true}}
			Expect(ph.Patch(ctx, byoHost)).To(Succeed())

			updated := &byohv1beta1.ByoHost{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: "host1"}, updated)).To(Succeed())
			Expect(updated.Status.HostDetails.Architecture).To(Equal("arm64"))
		})

		It("should not let the agents update a ByoHost other than its status with RBAC", func() {
			byoHost.Labels["role"] = "control-plane"
			err := ValidUserK8sClient.Update(ctx, byoHost)
			Expect(apierrors.IsForbidden(err)).To(BeTrue(), "%v", err)
			Expect(err.Error()).To(ContainSubstring(`User "byoh:host:host1" cannot update resource "byohosts"`))
			err = InvalidUserK8sClient.Update(ctx, byoHost)
			Expect(apierrors.IsForbidden(err)).To(BeTrue(), "%v", err)
		})

		It("should reject an agent updating a ByoHost other than its status in the webhook even when RBAC allows it", func() {
			host3 := newByoHost("host3")
			Expect(k8sClient.Create(ctx, host3)).To(Succeed())
			DeferCleanup(deleteByoHost, host3)
			grantToHost3("update")

			host3.Labels = map[string]string{"role": "control-plane"}
			Eventually(func() string {
				err := Host3K8sClient.Update(ctx, host3)
				if err == nil {
					return ""
				}
				return err.Error()
			}).Should(Equal(denied("byoh:host:host3 cannot update ByoHost host3: a member of byoh:hosts can update only the status of a ByoHost")))
		})

		It("should reject the agent updating the status of another host", func() {
			host2 := newByoHost("host2")
			Expect(k8sClient.Create(ctx, host2)).To(Succeed())
			DeferCleanup(deleteByoHost, host2)

			host2.Status.LastReset = &byohv1beta1.LastReset{ByoMachineUID: "uid-of-byomachine1", Time: metav1.Now()}
			err := ValidUserK8sClient.Status().Update(ctx, host2)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("byoh:host:host1 cannot update the status of ByoHost host2"))
		})

		It("should allow the request from an administrator", func() {
			byoHost.Labels["role"] = "control-plane"
			Expect(k8sClient.Update(ctx, byoHost)).Should(Succeed())
		})
	})
})
