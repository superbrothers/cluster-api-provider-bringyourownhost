// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package registration_test

import (
	"context"
	"runtime"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/superbrothers/cluster-api-provider-bringyourownhost/agent/registration"
	"github.com/superbrothers/cluster-api-provider-bringyourownhost/agent/version"
	infrastructurev1beta1 "github.com/superbrothers/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/superbrothers/cluster-api-provider-bringyourownhost/test/builder"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Host Registrar Tests", func() {
	var (
		hr               registration.HostRegistrar
		byoHost          *infrastructurev1beta1.ByoHost
		defaultNamespace = "default"
		ctx              = context.TODO()
	)

	BeforeEach(func() {
		hr = registration.HostRegistrar{K8sClient: k8sClient}
		byoHost = builder.ByoHost(defaultNamespace, "host").Build()
		Expect(k8sClient.Create(ctx, byoHost)).Should(Succeed())
	})

	AfterEach(func() {
		Expect(k8sClient.Delete(ctx, byoHost)).ToNot(HaveOccurred())
	})

	Context("When a ByoHost exists and registration is done", func() {
		It("Should update the host details on the byohost successfully", func() {
			Expect(hr.UpdateHost(ctx, byoHost)).ToNot(HaveOccurred())
		})

		It("Should report the version of the agent in the agent info", func() {
			gitVersion := version.GitVersion
			DeferCleanup(func() { version.GitVersion = gitVersion })
			version.GitVersion = "v0.6.0-rc.0"

			Expect(hr.UpdateHost(ctx, byoHost)).To(Succeed())

			// Read it back, so that the CRD schema keeps the field.
			updated := &infrastructurev1beta1.ByoHost{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(byoHost), updated)).To(Succeed())
			Expect(updated.Status.AgentInfo).NotTo(BeNil())
			Expect(updated.Status.AgentInfo.Version).To(Equal("v0.6.0-rc.0"))
			Expect(updated.Status.HostDetails.Architecture).To(Equal(runtime.GOARCH))
		})

		It("Should report a version without a release tag for an agent built without a version", func() {
			gitVersion := version.GitVersion
			DeferCleanup(func() { version.GitVersion = gitVersion })
			version.GitVersion = ""

			Expect(hr.UpdateHost(ctx, byoHost)).To(Succeed())

			updated := &infrastructurev1beta1.ByoHost{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(byoHost), updated)).To(Succeed())
			Expect(updated.Status.AgentInfo).NotTo(BeNil())
			// The version comes from the commit in the build information of the
			// binary, which a test binary may not have.
			Expect(updated.Status.AgentInfo.Version).To(MatchRegexp(`^v0\.0\.0-([0-9a-f]{14}(-dirty)?|unknown)$`))
		})
	})

	Context("When the agent info has no version", func() {
		It("Should reject an empty version", func() {
			byoHost.Status.AgentInfo = &infrastructurev1beta1.AgentInfo{Version: ""}
			err := k8sClient.Status().Update(ctx, byoHost)
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected an invalid error, got %v", err)
			Expect(err.Error()).To(ContainSubstring("status.agentInfo.version"))
		})

		It("Should reject the agent info without a version", func() {
			// The typed client always sends the version, so send the object
			// without it.
			patch := []byte(`{"status":{"agentInfo":{}}}`)
			err := k8sClient.Status().Patch(ctx, byoHost, client.RawPatch(types.MergePatchType, patch))
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected an invalid error, got %v", err)
			Expect(err.Error()).To(ContainSubstring("status.agentInfo.version"))
		})
	})
})
