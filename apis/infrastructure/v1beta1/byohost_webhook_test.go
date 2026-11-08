// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1_test

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	byohv1beta1 "github.com/superbrothers/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/kubectl/pkg/scheme"
	v1beta1patch "sigs.k8s.io/cluster-api/util/deprecated/v1beta1/patch"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("ByohostWebhook", func() {

	Context("When ByoHost gets a delete request", func() {
		var (
			byoHost *byohv1beta1.ByoHost
		)
		BeforeEach(func() {
			ctx = context.Background()
			byoHost = &byohv1beta1.ByoHost{
				TypeMeta: metav1.TypeMeta{
					Kind:       "ByoHost",
					APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "host1",
					Namespace: "default",
				},
				Spec: byohv1beta1.ByoHostSpec{},
			}
			Expect(ValidUserK8sClient.Create(ctx, byoHost)).Should(Succeed())
		})

		It("should not reject the request", func() {
			err := ValidUserK8sClient.Delete(ctx, byoHost)
			Expect(err).To(BeNil())
		})

		Context("When ByoHost is attached to a ByoMachine", func() {
			var (
				byoMachine        *byohv1beta1.ByoMachine
				k8sClientUncached client.Client
			)
			BeforeEach(func() {
				var clientErr error
				k8sClientUncached, clientErr = client.New(cfg, client.Options{Scheme: scheme.Scheme})
				Expect(clientErr).NotTo(HaveOccurred())
				byoMachine = &byohv1beta1.ByoMachine{
					TypeMeta: metav1.TypeMeta{
						Kind:       "ByoMachine",
						APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
					},
					ObjectMeta: metav1.ObjectMeta{
						GenerateName: "byomachine-",
						Namespace:    "default",
					},
					Spec: byohv1beta1.ByoMachineSpec{},
				}
				Expect(k8sClientUncached.Create(ctx, byoMachine)).Should(Succeed())

				// The controller attaches the ByoHost.
				ph, err := v1beta1patch.NewHelper(byoHost, k8sClientUncached)
				Expect(err).ShouldNot(HaveOccurred())
				byoHost.Spec.ByoMachineRef = &byohv1beta1.ByoMachineReference{
					Name: byoMachine.Name,
					UID:  byoMachine.UID,
				}
				Expect(ph.Patch(ctx, byoHost, v1beta1patch.WithStatusObservedGeneration{})).Should(Succeed())
			})

			It("should reject the request", func() {
				err := ValidUserK8sClient.Delete(ctx, byoHost)
				Expect(err).To(HaveOccurred())
				Expect(err).To(MatchError(fmt.Sprintf("admission webhook \"vbyohost.kb.io\" denied the request: cannot delete ByoHost %s while it is attached to ByoMachine %s (spec.byoMachineRef)", byoHost.Name, byoMachine.Name)))
			})

			AfterEach(func() {
				// delete the byohost resource
				ph, err := v1beta1patch.NewHelper(byoHost, k8sClientUncached)
				Expect(err).ShouldNot(HaveOccurred())
				byoHost.Spec.ByoMachineRef = nil
				Expect(ph.Patch(ctx, byoHost, v1beta1patch.WithStatusObservedGeneration{})).Should(Succeed())
				Expect(ValidUserK8sClient.Delete(ctx, byoHost)).Should(Succeed())
			})
		})
	})
	Context("When ByoHost gets a create request", func() {
		var (
			byoHost *byohv1beta1.ByoHost
		)
		BeforeEach(func() {
			ctx = context.Background()
			byoHost = &byohv1beta1.ByoHost{
				TypeMeta: metav1.TypeMeta{
					Kind:       "ByoHost",
					APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "host1",
					Namespace: "default",
				},
				Spec: byohv1beta1.ByoHostSpec{},
			}
		})

		It("should allow the request from a valid user", func() {
			Expect(ValidUserK8sClient.Create(ctx, byoHost)).Should(Succeed())
			// cleanup
			Expect(ValidUserK8sClient.Delete(ctx, byoHost)).Should(Succeed())
		})

		It("should reject the request from an invalid user", func() {
			err := InvalidUserK8sClient.Create(ctx, byoHost)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("test-user cannot create/update ByoHost host1"))
		})

		It("should reject the request from the agent of another host whose name is contained", func() {
			byoHost.Name = "host10"
			err := ValidUserK8sClient.Create(ctx, byoHost)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("byoh:host:host1 cannot create/update ByoHost host10"))
		})

		It("should allow the request from an administrator", func() {
			Expect(k8sClient.Create(ctx, byoHost)).Should(Succeed())
			// cleanup
			Expect(k8sClient.Delete(ctx, byoHost)).Should(Succeed())
		})
	})
	Context("When ByoHost gets a update request", func() {
		var (
			byoHost *byohv1beta1.ByoHost
		)
		BeforeEach(func() {
			ctx = context.Background()
			byoHost = &byohv1beta1.ByoHost{
				TypeMeta: metav1.TypeMeta{
					Kind:       "ByoHost",
					APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "host1",
					Namespace: "default",
				},
				Spec: byohv1beta1.ByoHostSpec{},
			}
			Expect(ValidUserK8sClient.Create(ctx, byoHost)).Should(Succeed())
		})
		It("should allow the agent to update the status of its ByoHost", func() {
			arch := "amd64"
			byoHost.Status.HostDetails.Architecture = arch
			Expect(ValidUserK8sClient.Status().Update(ctx, byoHost)).Should(Succeed())

			updatedByoHost := &byohv1beta1.ByoHost{}
			Expect(ValidUserK8sClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: "host1"}, updatedByoHost)).Should(Succeed())
			Expect(updatedByoHost.Status.HostDetails.Architecture).To(Equal(arch))
		})
		It("should forbid the agents to update a ByoHost other than its status", func() {
			// The agent writes only the status after it creates its ByoHost.
			byoHost.Labels = map[string]string{"role": "control-plane"}
			err := ValidUserK8sClient.Update(ctx, byoHost)
			Expect(apierrors.IsForbidden(err)).To(BeTrue(), "unexpected error: %v", err)
			err = InvalidUserK8sClient.Update(ctx, byoHost)
			Expect(apierrors.IsForbidden(err)).To(BeTrue(), "unexpected error: %v", err)
		})
		It("should allow the request from an administrator", func() {
			byoHost.Labels = map[string]string{"role": "worker"}
			Expect(k8sClient.Update(ctx, byoHost)).Should(Succeed())
		})
		AfterEach(func() {
			Expect(ValidUserK8sClient.Delete(ctx, byoHost)).Should(Succeed())
		})
	})
})
