// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package v1beta1_test

import (
	"context"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/superbrothers/cluster-api-provider-bringyourownhost/agent/cloudinit/cloudinitfakes"
	"github.com/superbrothers/cluster-api-provider-bringyourownhost/agent/reconciler"
	"github.com/superbrothers/cluster-api-provider-bringyourownhost/agent/registration"
	byohv1beta1 "github.com/superbrothers/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/superbrothers/cluster-api-provider-bringyourownhost/test/builder"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	v1beta1conditions "sigs.k8s.io/cluster-api/util/deprecated/v1beta1/conditions"
	v1beta1patch "sigs.k8s.io/cluster-api/util/deprecated/v1beta1/patch"
	ctrl "sigs.k8s.io/controller-runtime"
)

// These tests run the code of the agent with the client of a host against the
// webhook and the RBAC of the agents, so that neither rejects what the agent
// writes.
var _ = Describe("ByohostWebhook with the agent", func() {
	var (
		key                 = types.NamespacedName{Namespace: "default", Name: "host1"}
		cmdRunner           *cloudinitfakes.FakeICmdRunner
		hostReconciler      *reconciler.HostReconciler
		registrar           *registration.HostRegistrar
		uninstallScriptPath string
	)

	getByoHost := func() *byohv1beta1.ByoHost {
		byoHost := &byohv1beta1.ByoHost{}
		Expect(k8sClient.Get(ctx, key, byoHost)).To(Succeed())
		return byoHost
	}
	reconcile := func() {
		_, err := hostReconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
	}
	// patchSpec changes the spec of the ByoHost as the controller does.
	patchSpec := func(mutate func(*byohv1beta1.ByoHostSpec)) {
		byoHost := getByoHost()
		ph, err := v1beta1patch.NewHelper(byoHost, k8sClient)
		Expect(err).NotTo(HaveOccurred())
		mutate(&byoHost.Spec)
		Expect(ph.Patch(ctx, byoHost)).To(Succeed())
	}

	BeforeEach(func() {
		ctx = context.Background()
		cmdRunner = &cloudinitfakes.FakeICmdRunner{}
		uninstallScriptPath = filepath.Join(GinkgoT().TempDir(), "uninstall.sh")
		hostReconciler = &reconciler.HostReconciler{
			Client:              ValidUserK8sClient,
			CmdRunner:           cmdRunner,
			FileWriter:          &cloudinitfakes.FakeIFileWriter{},
			TemplateParser:      &cloudinitfakes.FakeITemplateParser{},
			Recorder:            events.NewFakeRecorder(100),
			UninstallScriptPath: uninstallScriptPath,
		}
		registrar = &registration.HostRegistrar{K8sClient: ValidUserK8sClient}

		bootstrapSecret := builder.Secret("default", "byomachine1").WithData("runCmd:\n- echo bootstrap\n").Build()
		bootstrapSecret.GenerateName = ""
		bootstrapSecret.Name = "byomachine1"
		Expect(k8sClient.Create(ctx, bootstrapSecret)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, bootstrapSecret)).To(Succeed()) })
		installationSecret := builder.Secret("default", "byomachine1-installation").
			WithKeyData("install", "install").
			WithKeyData("uninstall", "uninstall").
			Build()
		installationSecret.GenerateName = ""
		installationSecret.Name = "byomachine1-installation"
		Expect(k8sClient.Create(ctx, installationSecret)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, installationSecret)).To(Succeed()) })
	})
	AfterEach(func() {
		deleteByoHost(newByoHost("host1"))
	})

	DescribeTable("should let the agent register the host, provision it and reset it",
		func(hostLabels map[string]string) {
			By("registering the host")
			Expect(registrar.Register(key.Name, key.Namespace, hostLabels)).To(Succeed())
			byoHost := getByoHost()
			Expect(byoHost.Status.HostDetails.OSName).NotTo(BeEmpty())
			Expect(byoHost.Status.AgentInfo).NotTo(BeNil())

			By("reconciling the host while it is free")
			reconcile()
			Expect(v1beta1conditions.GetReason(getByoHost(), byohv1beta1.K8sNodeBootstrapSucceeded)).To(Equal(byohv1beta1.WaitingForByoMachineRefReason))

			By("attaching the host as the controller does")
			patchSpec(func(spec *byohv1beta1.ByoHostSpec) {
				spec.ByoMachineRef = &byohv1beta1.ByoMachineReference{Name: "byomachine1", UID: "uid-of-byomachine1"}
				spec.BootstrapSecret = &corev1.ObjectReference{Kind: "Secret", Namespace: "default", Name: "byomachine1"}
				spec.InstallationSecret = &corev1.ObjectReference{Kind: "Secret", Namespace: "default", Name: "byomachine1-installation"}
			})

			By("installing the components and bootstrapping the node")
			reconcile()
			byoHost = getByoHost()
			Expect(v1beta1conditions.IsTrue(byoHost, byohv1beta1.K8sComponentsInstallationSucceeded)).To(BeTrue())
			Expect(v1beta1conditions.IsTrue(byoHost, byohv1beta1.K8sNodeBootstrapSucceeded)).To(BeTrue())
			Expect(uninstallScriptPath).To(BeAnExistingFile())

			By("requesting the reset as the controller does")
			patchSpec(func(spec *byohv1beta1.ByoHostSpec) {
				spec.ResetRequested = true
			})

			By("resetting the host")
			calls := cmdRunner.RunCmdCallCount()
			reconcile()
			Expect(cmdRunner.RunCmdCallCount()).To(Equal(calls + 2))
			_, resetCommand := cmdRunner.RunCmdArgsForCall(calls)
			Expect(resetCommand).To(Equal(reconciler.KubeadmResetCommand))
			_, uninstallCommand := cmdRunner.RunCmdArgsForCall(calls + 1)
			Expect(uninstallCommand).To(Equal("uninstall"))
			byoHost = getByoHost()
			Expect(byoHost.Status.LastReset).NotTo(BeNil())
			Expect(byoHost.Status.LastReset.ByoMachineUID).To(Equal(byoHost.Spec.ByoMachineRef.UID))

			By("detaching the host as the controller does")
			patchSpec(func(spec *byohv1beta1.ByoHostSpec) {
				*spec = byohv1beta1.ByoHostSpec{}
			})

			By("reconciling the host after it is free again")
			reconcile()
			byoHost = getByoHost()
			Expect(v1beta1conditions.GetReason(byoHost, byohv1beta1.K8sNodeBootstrapSucceeded)).To(Equal(byohv1beta1.WaitingForByoMachineRefReason))
			Expect(v1beta1conditions.Get(byoHost, byohv1beta1.K8sComponentsInstallationSucceeded)).To(BeNil())

			// The agent has changed only the status of its ByoHost.
			if len(hostLabels) == 0 {
				Expect(byoHost.Labels).To(BeEmpty())
			} else {
				Expect(byoHost.Labels).To(Equal(hostLabels))
			}
			Expect(byoHost.Annotations).To(BeEmpty())
			Expect(byoHost.Spec).To(Equal(byohv1beta1.ByoHostSpec{}))

			By("registering the host again when the agent restarts")
			Expect(registrar.Register(key.Name, key.Namespace, hostLabels)).To(Succeed())
		},
		Entry("with the labels of --label", map[string]string{"role": "worker"}),
		Entry("without --label", nil),
	)
})
