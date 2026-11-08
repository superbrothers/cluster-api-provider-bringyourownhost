// Copyright 2022 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"context"
	"encoding/json"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	admissionv1 "k8s.io/api/admission/v1"
	v1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var _ = Describe("ByohostWebhook/Unit", func() {
	schema := runtime.NewScheme()
	err := AddToScheme(schema)
	Expect(err).NotTo(HaveOccurred())
	v := NewByoHostValidator(schema)

	newByoHost := func(name string) *ByoHost {
		return &ByoHost{
			TypeMeta: metav1.TypeMeta{
				Kind:       "ByoHost",
				APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
			},
			Spec: ByoHostSpec{},
		}
	}
	rawExtension := func(byoHost *ByoHost) runtime.RawExtension {
		raw, marshalErr := json.Marshal(byoHost)
		Expect(marshalErr).ShouldNot(HaveOccurred())
		return runtime.RawExtension{Raw: raw, Object: byoHost}
	}
	// newRequest returns a request of the operation on the subresource of the
	// ByoHost by the user. A deletion has the ByoHost as the old object, and
	// the others as the object.
	newRequest := func(operation admissionv1.Operation, subResource string, byoHost *ByoHost, user v1.UserInfo) admission.Request {
		request := admissionv1.AdmissionRequest{
			Operation:   operation,
			SubResource: subResource,
			UserInfo:    user,
		}
		if operation == admissionv1.Delete {
			request.OldObject = rawExtension(byoHost)
		} else {
			request.Object = rawExtension(byoHost)
			if operation == admissionv1.Update {
				request.OldObject = rawExtension(byoHost)
			}
		}
		return admission.Request{AdmissionRequest: request}
	}
	agent := func(name string) v1.UserInfo {
		return v1.UserInfo{Username: name, Groups: []string{"byoh:hosts", "system:authenticated"}}
	}
	nameDeniedMessage := func(userName, verb, name string) string {
		return fmt.Sprintf("%s cannot %s ByoHost %s: a member of byoh:hosts can create, and update the status of, only the ByoHost named after its username byoh:host:<name>", userName, verb, name)
	}
	notHosts := []TableEntry{
		Entry("an administrator", v1.UserInfo{Username: "kubernetes-admin", Groups: []string{"kubeadm:cluster-admins", "system:authenticated"}}),
		Entry("the manager", v1.UserInfo{Username: "system:serviceaccount:byoh-system:byoh-controller-manager", Groups: []string{"system:serviceaccounts", "system:serviceaccounts:byoh-system", "system:authenticated"}}),
		Entry("a user with the username of an agent", v1.UserInfo{Username: "byoh:host:host2"}),
		Entry("a username of two parts", v1.UserInfo{Username: "system:admin"}),
	}

	// The requests to create a ByoHost and to update its status are both
	// limited to the ByoHost of the agent.
	for _, request := range []struct {
		operation   admissionv1.Operation
		subResource string
		verb        string
	}{
		{admissionv1.Create, "", "create"},
		{admissionv1.Update, "status", updateStatusVerb},
	} {
		Context(fmt.Sprintf("When ByoHost gets a request to %s it", request.verb), func() {
			It("Should allow the request from the agent of the host", func() {
				resp := v.Handle(context.TODO(), newRequest(request.operation, request.subResource, newByoHost("host1"), agent("byoh:host:host1")))
				Expect(resp.AdmissionResponse.Allowed).To(BeTrue())
			})

			DescribeTable("Should reject the request from an agent of another host",
				func(userName, name string) {
					resp := v.Handle(context.TODO(), newRequest(request.operation, request.subResource, newByoHost(name), agent(userName)))
					Expect(resp.AdmissionResponse.Allowed).To(BeFalse())
					Expect(resp.AdmissionResponse.Result.Message).To(Equal(nameDeniedMessage(userName, request.verb, name)))
				},
				Entry("another host", "byoh:host:host2", "host1"),
				Entry("a host whose name contains the name of the agent's host", "byoh:host:host1", "host10"),
				Entry("a host whose name is contained in the name of the agent's host", "byoh:host:host10", "host1"),
			)

			DescribeTable("Should reject the request from a member of byoh:hosts without the username of an agent",
				func(userName string) {
					var resp admission.Response
					Expect(func() {
						resp = v.Handle(context.TODO(), newRequest(request.operation, request.subResource, newByoHost("host1"), agent(userName)))
					}).NotTo(Panic())
					Expect(resp.AdmissionResponse.Allowed).To(BeFalse())
					Expect(resp.AdmissionResponse.Result.Message).To(Equal(nameDeniedMessage(userName, request.verb, "host1")))
				},
				Entry("a username without colons", "test-user"),
				Entry("a username of two parts", "byoh:host"),
				Entry("the prefix only", "byoh:host:"),
				Entry("a username with another prefix", "system:node:host1"),
			)

			DescribeTable("Should leave the request from a user that is not a member of byoh:hosts to RBAC",
				func(user v1.UserInfo) {
					var resp admission.Response
					Expect(func() {
						resp = v.Handle(context.TODO(), newRequest(request.operation, request.subResource, newByoHost("host1"), user))
					}).NotTo(Panic())
					Expect(resp.AdmissionResponse.Allowed).To(BeTrue())
				},
				notHosts,
			)
		})
	}

	Context("When ByoHost gets a request to create it", func() {
		It("Should allow the agent to create its ByoHost with the labels of --label", func() {
			byoHost := newByoHost("host1")
			byoHost.Labels = map[string]string{"role": "worker", "example.com/site": "tokyo"}
			resp := v.Handle(context.TODO(), newRequest(admissionv1.Create, "", byoHost, agent("byoh:host:host1")))
			Expect(resp.AdmissionResponse.Allowed).To(BeTrue())
		})

		DescribeTable("Should reject a ByoHost with what only the controller and the administrators set",
			func(mutate func(*ByoHost), field string) {
				byoHost := newByoHost("host1")
				mutate(byoHost)
				resp := v.Handle(context.TODO(), newRequest(admissionv1.Create, "", byoHost, agent("byoh:host:host1")))
				Expect(resp.AdmissionResponse.Allowed).To(BeFalse())
				Expect(resp.AdmissionResponse.Result.Message).To(HavePrefix("byoh:host:host1 cannot create ByoHost host1: " + field))
			},
			Entry("a label of Cluster API", func(h *ByoHost) {
				h.Labels = map[string]string{"cluster.x-k8s.io/cluster-name": "cluster1"}
			}, "metadata.labels[cluster.x-k8s.io/cluster-name]"),
			Entry("a label of BYOH", func(h *ByoHost) {
				h.Labels = map[string]string{"byoh.infrastructure.cluster.x-k8s.io/byomachine-name": "byomachine1"}
			}, "metadata.labels[byoh.infrastructure.cluster.x-k8s.io/byomachine-name]"),
			Entry("an annotation", func(h *ByoHost) {
				h.Annotations = map[string]string{"cluster.x-k8s.io/paused": ""}
			}, "metadata.annotations"),
			Entry("an owner reference", func(h *ByoHost) {
				h.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Secret", Name: "secret1", UID: "uid-of-secret1"}}
			}, "metadata.ownerReferences"),
			Entry("a finalizer", func(h *ByoHost) {
				h.Finalizers = []string{"example.com/finalizer"}
			}, "metadata.finalizers"),
			Entry("the ByoMachine", func(h *ByoHost) {
				h.Spec.ByoMachineRef = &ByoMachineReference{Name: "byomachine1", UID: "uid-of-byomachine1"}
			}, "spec"),
			Entry("a request of a reset", func(h *ByoHost) {
				h.Spec.ResetRequested = true
			}, "spec"),
			Entry("the bootstrap data", func(h *ByoHost) {
				h.Spec.BootstrapSecret = &corev1.ObjectReference{Kind: "Secret", Namespace: "default", Name: "byomachine1"}
			}, "spec"),
		)

		It("Should not take a label whose prefix only ends with cluster.x-k8s.io as one of Cluster API", func() {
			byoHost := newByoHost("host1")
			byoHost.Labels = map[string]string{"examplecluster.x-k8s.io/role": "worker"}
			resp := v.Handle(context.TODO(), newRequest(admissionv1.Create, "", byoHost, agent("byoh:host:host1")))
			Expect(resp.AdmissionResponse.Allowed).To(BeTrue())
		})
	})

	Context("When ByoHost gets a request to update it other than its status", func() {
		It("Should reject the request from the agent of the host", func() {
			resp := v.Handle(context.TODO(), newRequest(admissionv1.Update, "", newByoHost("host1"), agent("byoh:host:host1")))
			Expect(resp.AdmissionResponse.Allowed).To(BeFalse())
			Expect(resp.AdmissionResponse.Result.Message).To(Equal("byoh:host:host1 cannot update ByoHost host1: a member of byoh:hosts can update only the status of a ByoHost"))
		})

		DescribeTable("Should leave the request from a user that is not a member of byoh:hosts to RBAC",
			func(user v1.UserInfo) {
				resp := v.Handle(context.TODO(), newRequest(admissionv1.Update, "", newByoHost("host1"), user))
				Expect(resp.AdmissionResponse.Allowed).To(BeTrue())
			},
			notHosts,
		)
	})

	Context("When ByoHost gets a request to delete it", func() {
		It("Should reject the request from the agent of the host", func() {
			resp := v.Handle(context.TODO(), newRequest(admissionv1.Delete, "", newByoHost("host1"), agent("byoh:host:host1")))
			Expect(resp.AdmissionResponse.Allowed).To(BeFalse())
			Expect(resp.AdmissionResponse.Result.Message).To(Equal("byoh:host:host1 cannot delete ByoHost host1: a member of byoh:hosts cannot delete ByoHosts"))
		})

		DescribeTable("Should leave the request from a user that is not a member of byoh:hosts to RBAC",
			func(user v1.UserInfo) {
				resp := v.Handle(context.TODO(), newRequest(admissionv1.Delete, "", newByoHost("host1"), user))
				Expect(resp.AdmissionResponse.Allowed).To(BeTrue())
			},
			notHosts,
		)

		It("Should reject the request from any user when the ByoHost is attached to a ByoMachine", func() {
			byoHost := newByoHost("host1")
			byoHost.Spec.ByoMachineRef = &ByoMachineReference{
				Name: "byomachine1",
				UID:  "uid-of-byomachine1",
			}
			resp := v.Handle(context.TODO(), newRequest(admissionv1.Delete, "", byoHost, v1.UserInfo{Username: "kubernetes-admin", Groups: []string{"kubeadm:cluster-admins", "system:authenticated"}}))
			Expect(resp.AdmissionResponse.Allowed).To(BeFalse())
			Expect(resp.AdmissionResponse.Result.Message).To(Equal("cannot delete ByoHost host1 while it is attached to ByoMachine byomachine1 (spec.byoMachineRef)"))
		})
	})
})
