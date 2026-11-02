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

	// newRequest returns a request to create or update the ByoHost of the
	// given name by the given user.
	newRequest := func(operation admissionv1.Operation, name string, user v1.UserInfo) admission.Request {
		byoHost := &ByoHost{
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
		byoHostRaw, marshalErr := json.Marshal(byoHost)
		Expect(marshalErr).ShouldNot(HaveOccurred())
		return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: operation,
			UserInfo:  user,
			Object: runtime.RawExtension{
				Raw:    byoHostRaw,
				Object: byoHost,
			},
		}}
	}
	agent := func(name string) v1.UserInfo {
		return v1.UserInfo{Username: name, Groups: []string{"byoh:hosts", "system:authenticated"}}
	}
	deniedMessage := func(userName, name string) string {
		return fmt.Sprintf("%s cannot create/update ByoHost %s: a member of byoh:hosts can create/update only the ByoHost named after its username byoh:host:<name>", userName, name)
	}

	for _, operation := range []admissionv1.Operation{admissionv1.Create, admissionv1.Update} {
		Context(fmt.Sprintf("When ByoHost gets a %s request", operation), func() {
			It("Should allow the request from the agent of the host", func() {
				resp := v.Handle(context.TODO(), newRequest(operation, "host1", agent("byoh:host:host1")))
				Expect(resp.AdmissionResponse.Allowed).To(BeTrue())
			})

			DescribeTable("Should reject the request from an agent of another host",
				func(userName, name string) {
					resp := v.Handle(context.TODO(), newRequest(operation, name, agent(userName)))
					Expect(resp.AdmissionResponse.Allowed).To(BeFalse())
					Expect(resp.AdmissionResponse.Result.Message).To(Equal(deniedMessage(userName, name)))
				},
				Entry("another host", "byoh:host:host2", "host1"),
				Entry("a host whose name contains the name of the agent's host", "byoh:host:host1", "host10"),
				Entry("a host whose name is contained in the name of the agent's host", "byoh:host:host10", "host1"),
			)

			DescribeTable("Should reject the request from a member of byoh:hosts without the username of an agent",
				func(userName string) {
					var resp admission.Response
					Expect(func() {
						resp = v.Handle(context.TODO(), newRequest(operation, "host1", agent(userName)))
					}).NotTo(Panic())
					Expect(resp.AdmissionResponse.Allowed).To(BeFalse())
					Expect(resp.AdmissionResponse.Result.Message).To(Equal(deniedMessage(userName, "host1")))
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
						resp = v.Handle(context.TODO(), newRequest(operation, "host1", user))
					}).NotTo(Panic())
					Expect(resp.AdmissionResponse.Allowed).To(BeTrue())
				},
				Entry("an administrator", v1.UserInfo{Username: "kubernetes-admin", Groups: []string{"kubeadm:cluster-admins", "system:authenticated"}}),
				Entry("the manager", v1.UserInfo{Username: "system:serviceaccount:byoh-system:byoh-controller-manager", Groups: []string{"system:serviceaccounts", "system:serviceaccounts:byoh-system", "system:authenticated"}}),
				Entry("a user with the username of an agent", v1.UserInfo{Username: "byoh:host:host2"}),
				Entry("a username of two parts", v1.UserInfo{Username: "system:admin"}),
			)
		})
	}

	Context("When ByoHost gets an delete request", func() {
		var (
			byoHost    *ByoHost
			byoHostRaw []byte
			ctx        context.Context
		)
		BeforeEach(func() {
			ctx = context.TODO()
			byoHost = &ByoHost{
				TypeMeta: metav1.TypeMeta{
					Kind:       "ByoHost",
					APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "host1",
					Namespace: "default",
				},
				Spec: ByoHostSpec{},
			}
			byoHostRaw, err = json.Marshal(byoHost)
			Expect(err).ShouldNot(HaveOccurred())
		})
		It("Should allow delete request from any user", func() {
			admissionRequest := admissionv1.AdmissionRequest{
				Operation: admissionv1.Delete,
				UserInfo:  v1.UserInfo{Username: "random-user"},
				OldObject: runtime.RawExtension{
					Raw:    byoHostRaw,
					Object: byoHost,
				},
			}
			resp := v.Handle(ctx, admission.Request{AdmissionRequest: admissionRequest})
			Expect(resp.AdmissionResponse.Allowed).To(Equal(true))
		})
		It("Should reject delete request if status.MachineRef is not nil", func() {
			byoHost.Status.MachineRef = &corev1.ObjectReference{
				Kind:       "ByoMachine",
				Namespace:  "default",
				Name:       "byomachine1",
				APIVersion: byoHost.APIVersion,
			}
			byoHostRaw, err = json.Marshal(byoHost)
			Expect(err).ShouldNot(HaveOccurred())
			admissionRequest := admissionv1.AdmissionRequest{
				Operation: admissionv1.Delete,
				UserInfo:  v1.UserInfo{Username: "random-user"},
				OldObject: runtime.RawExtension{
					Raw:    byoHostRaw,
					Object: byoHost,
				},
			}
			resp := v.Handle(ctx, admission.Request{AdmissionRequest: admissionRequest})
			Expect(resp.AdmissionResponse.Allowed).To(Equal(false))
			Expect(resp.AdmissionResponse.Result.Message).To(Equal("cannot delete ByoHost when MachineRef is assigned"))
		})
	})
})
