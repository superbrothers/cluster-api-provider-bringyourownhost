// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"context"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"net"
	"net/url"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	infrastructurev1beta1 "github.com/superbrothers/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/superbrothers/cluster-api-provider-bringyourownhost/test/builder"
	certv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var _ = Describe("Controllers/ByoadmissionController", func() {
	const (
		hostName = "my-host"
		csrName  = "byoh-csr-" + hostName
		// bootstrapUser is the user of a bootstrap token of BYOH.
		bootstrapUser = "system:bootstrap:abcdef"
	)
	var (
		hostUser        = infrastructurev1beta1.HostUsernamePrefix + hostName
		bootstrapGroups = []string{"system:bootstrappers", infrastructurev1beta1.BootstrapTokenExtraGroups, "system:authenticated"}
		hostGroups      = []string{infrastructurev1beta1.HostsGroup, "system:authenticated"}
	)

	// hostCSR returns a builder of the CSR that an agent creates with a
	// bootstrap token to register its host.
	hostCSR := func() *builder.CertificateSigningRequestBuilder {
		return builder.CertificateSigningRequest(csrName, hostUser, infrastructurev1beta1.HostsGroup, 2048).
			WithRequester(bootstrapUser, bootstrapGroups...)
	}

	// reconcileCSR creates the CSR, reconciles it, and returns the result.
	reconcileCSR := func(csrBuilder *builder.CertificateSigningRequestBuilder) *certv1.CertificateSigningRequest {
		csr, err := csrBuilder.Build()
		Expect(err).NotTo(HaveOccurred())
		_, err = clientSetFake.CertificatesV1().CertificateSigningRequests().Create(ctx, csr, v1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			Expect(clientSetFake.CertificatesV1().CertificateSigningRequests().Delete(ctx, csr.Name, v1.DeleteOptions{})).To(Succeed())
		})

		_, err = byoAdmissionReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: csr.Name}})
		Expect(err).NotTo(HaveOccurred())

		updated, err := clientSetFake.CertificatesV1().CertificateSigningRequests().Get(ctx, csr.Name, v1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		return updated
	}

	approved := certv1.CertificateSigningRequestCondition{
		Type:   certv1.CertificateApproved,
		Reason: "Approved by ByoAdmission Controller",
		Status: corev1.ConditionTrue,
	}

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("should return error for non-existent CSR", func() {
		// Call Reconcile method for a non-existing CSR
		objectKey := types.NamespacedName{Name: defaultByoHostName}
		_, err := byoAdmissionReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objectKey})
		Expect(err).To(BeNil())
	})

	It("should approve the CSR of a host that registers with a bootstrap token", func() {
		csr := reconcileCSR(hostCSR())
		Expect(csr.Status.Conditions).To(ConsistOf(approved))
	})

	It("should approve the CSR with the key usages that the kubelet requests", func() {
		csr := reconcileCSR(hostCSR().WithUsages(certv1.UsageDigitalSignature, certv1.UsageKeyEncipherment, certv1.UsageClientAuth))
		Expect(csr.Status.Conditions).To(ConsistOf(approved))
	})

	DescribeTable("should deny the CSR that is not one of a host",
		func(csrBuilder func() *builder.CertificateSigningRequestBuilder, message string) {
			csr := reconcileCSR(csrBuilder())
			Expect(csr.Status.Conditions).To(HaveLen(1))
			Expect(csr.Status.Conditions[0].Type).To(Equal(certv1.CertificateDenied))
			Expect(csr.Status.Conditions[0].Status).To(Equal(corev1.ConditionTrue))
			Expect(csr.Status.Conditions[0].Reason).To(Equal("ByohCSRInvalid"))
			Expect(csr.Status.Conditions[0].Message).To(ContainSubstring(message))
		},
		Entry("with another usage", func() *builder.CertificateSigningRequestBuilder {
			return hostCSR().WithUsages(certv1.UsageClientAuth, certv1.UsageServerAuth)
		}, "the usages must be one of"),
		Entry("with another organization", func() *builder.CertificateSigningRequestBuilder {
			return hostCSR().WithOrganizations("system:masters")
		}, "the organization must be only"),
		Entry("with an extra organization", func() *builder.CertificateSigningRequestBuilder {
			return hostCSR().WithOrganizations(infrastructurev1beta1.HostsGroup, "system:masters")
		}, "the organization must be only"),
		Entry("with a common name that is not of a host", func() *builder.CertificateSigningRequestBuilder {
			return builder.CertificateSigningRequest(csrName, "kubernetes-admin", infrastructurev1beta1.HostsGroup, 2048).
				WithRequester(bootstrapUser, bootstrapGroups...)
		}, "the common name must be"),
		Entry("with an invalid host name", func() *builder.CertificateSigningRequestBuilder {
			return builder.CertificateSigningRequest(csrName, infrastructurev1beta1.HostUsernamePrefix+"My_Host", infrastructurev1beta1.HostsGroup, 2048).
				WithRequester(bootstrapUser, bootstrapGroups...)
		}, "in the common name is invalid"),
		Entry("with an empty host name", func() *builder.CertificateSigningRequestBuilder {
			return builder.CertificateSigningRequest(csrName, infrastructurev1beta1.HostUsernamePrefix, infrastructurev1beta1.HostsGroup, 2048).
				WithRequester(bootstrapUser, bootstrapGroups...)
		}, "in the common name is invalid"),
		Entry("with an organizational unit", func() *builder.CertificateSigningRequestBuilder {
			return hostCSR().WithExtraNames(pkix.AttributeTypeAndValue{Type: asn1.ObjectIdentifier{2, 5, 4, 11}, Value: "system:masters"})
		}, "the subject must have only one organization and one common name"),
		Entry("with a user ID", func() *builder.CertificateSigningRequestBuilder {
			return hostCSR().WithExtraNames(pkix.AttributeTypeAndValue{Type: asn1.ObjectIdentifier{0, 9, 2342, 19200300, 100, 1, 1}, Value: "kubernetes-admin"})
		}, "the subject must have only one organization and one common name"),
		Entry("with a second common name", func() *builder.CertificateSigningRequestBuilder {
			return hostCSR().WithExtraNames(
				pkix.AttributeTypeAndValue{Type: asn1.ObjectIdentifier{2, 5, 4, 3}, Value: "kubernetes-admin"},
				pkix.AttributeTypeAndValue{Type: asn1.ObjectIdentifier{2, 5, 4, 3}, Value: hostUser},
			)
		}, "the subject must have only one organization and one common name"),
		Entry("with a DNS subject alternative name", func() *builder.CertificateSigningRequestBuilder {
			return hostCSR().WithDNSNames("example.com")
		}, "subject alternative names"),
		Entry("with an IP subject alternative name", func() *builder.CertificateSigningRequestBuilder {
			return hostCSR().WithIPAddresses(net.ParseIP("192.0.2.1"))
		}, "subject alternative names"),
		Entry("with an email subject alternative name", func() *builder.CertificateSigningRequestBuilder {
			return hostCSR().WithEmailAddresses("admin@example.com")
		}, "subject alternative names"),
		Entry("with a URI subject alternative name", func() *builder.CertificateSigningRequestBuilder {
			return hostCSR().WithURIs(&url.URL{Scheme: "spiffe", Host: "example.com", Path: "/admin"})
		}, "subject alternative names"),
		Entry("from a bootstrap token that is not of BYOH", func() *builder.CertificateSigningRequestBuilder {
			return hostCSR().WithRequester(bootstrapUser, "system:bootstrappers", "system:bootstrappers:kubeadm:default-node-token", "system:authenticated")
		}, "the requester"),
		Entry("from a user in the group of the bootstrap tokens of BYOH that is not a bootstrap token", func() *builder.CertificateSigningRequestBuilder {
			return hostCSR().WithRequester("kubernetes-admin", infrastructurev1beta1.BootstrapTokenExtraGroups)
		}, "the requester"),
		Entry("from the host itself, which renews its certificate with a bootstrap token for now", func() *builder.CertificateSigningRequestBuilder {
			return hostCSR().WithRequester(hostUser, hostGroups...)
		}, "the requester"),
		Entry("from another host", func() *builder.CertificateSigningRequestBuilder {
			return hostCSR().WithRequester(infrastructurev1beta1.HostUsernamePrefix+"another-host", hostGroups...)
		}, "the requester"),
		Entry("from another user", func() *builder.CertificateSigningRequestBuilder {
			return hostCSR().WithRequester("kubernetes-admin", "kubeadm:cluster-admins", "system:authenticated")
		}, "the requester"),
	)

	It("should truncate the values of the CSR in the message", func() {
		longName := strings.Repeat("x", 1000)
		csr := reconcileCSR(builder.CertificateSigningRequest(csrName, longName, infrastructurev1beta1.HostsGroup, 2048).
			WithRequester(bootstrapUser, bootstrapGroups...))
		Expect(csr.Status.Conditions).To(HaveLen(1))
		Expect(csr.Status.Conditions[0].Type).To(Equal(certv1.CertificateDenied))
		Expect(csr.Status.Conditions[0].Message).To(ContainSubstring(strings.Repeat("x", 64) + "..."))
		Expect(csr.Status.Conditions[0].Message).NotTo(ContainSubstring(strings.Repeat("x", 65)))
	})

	It("should return an error when it fails to update the approval", func() {
		reactionChain := clientSetFake.ReactionChain
		DeferCleanup(func() {
			clientSetFake.ReactionChain = reactionChain
		})
		clientSetFake.PrependReactor("update", "certificatesigningrequests", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if action.GetSubresource() != "approval" {
				return false, nil, nil
			}
			return true, nil, errors.New("failed to update the approval")
		})

		csr, err := hostCSR().Build()
		Expect(err).NotTo(HaveOccurred())
		_, err = clientSetFake.CertificatesV1().CertificateSigningRequests().Create(ctx, csr, v1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			Expect(clientSetFake.CertificatesV1().CertificateSigningRequests().Delete(ctx, csr.Name, v1.DeleteOptions{})).To(Succeed())
		})

		_, err = byoAdmissionReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: csr.Name}})
		Expect(err).To(MatchError("failed to update the approval"))
	})

	It("should leave the CSR for another signer", func() {
		csr := reconcileCSR(hostCSR().WithSignerName("kubernetes.io/kubelet-serving"))
		Expect(csr.Status.Conditions).To(BeEmpty())
	})

	It("should not approve a denied CSR", func() {
		denied := certv1.CertificateSigningRequestCondition{Type: certv1.CertificateDenied}
		csr, err := hostCSR().Build()
		Expect(err).NotTo(HaveOccurred())
		csr.Status.Conditions = append(csr.Status.Conditions, denied)
		_, err = clientSetFake.CertificatesV1().CertificateSigningRequests().Create(ctx, csr, v1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			Expect(clientSetFake.CertificatesV1().CertificateSigningRequests().Delete(ctx, csr.Name, v1.DeleteOptions{})).To(Succeed())
		})

		_, err = byoAdmissionReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: csr.Name}})
		Expect(err).NotTo(HaveOccurred())

		updated, err := clientSetFake.CertificatesV1().CertificateSigningRequests().Get(ctx, csr.Name, v1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(updated.Status.Conditions).To(ConsistOf(denied))
	})

	It("should not approve an already approved CSR again", func() {
		approvedBefore := certv1.CertificateSigningRequestCondition{Type: certv1.CertificateApproved}
		csr, err := hostCSR().Build()
		Expect(err).NotTo(HaveOccurred())
		csr.Status.Conditions = append(csr.Status.Conditions, approvedBefore)
		_, err = clientSetFake.CertificatesV1().CertificateSigningRequests().Create(ctx, csr, v1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			Expect(clientSetFake.CertificatesV1().CertificateSigningRequests().Delete(ctx, csr.Name, v1.DeleteOptions{})).To(Succeed())
		})

		_, err = byoAdmissionReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: csr.Name}})
		Expect(err).NotTo(HaveOccurred())

		updated, err := clientSetFake.CertificatesV1().CertificateSigningRequests().Get(ctx, csr.Name, v1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(updated.Status.Conditions).To(ConsistOf(approvedBefore))
	})
})
