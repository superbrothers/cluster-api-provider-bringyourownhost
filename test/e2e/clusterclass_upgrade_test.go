// Copyright 2022 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/cluster-api/test/framework"
	"sigs.k8s.io/cluster-api/test/framework/clusterctl"
	"sigs.k8s.io/cluster-api/util"
)

var _ = Describe("Clusterclass upgrade test [K8s-upgrade]", func() {

	var (
		ctx              context.Context
		specName         = "upgrade"
		namespace        *corev1.Namespace
		cancelWatches    context.CancelFunc
		clusterResources *clusterctl.ApplyClusterTemplateAndWaitResult
		hosts            *byoHosts
	)

	BeforeEach(func() {
		ctx = context.TODO()
		Expect(ctx).NotTo(BeNil(), "ctx is required for %s spec", specName)

		Expect(e2eConfig).NotTo(BeNil(), "Invalid argument. e2eConfig can't be nil when calling %s spec", specName)
		Expect(clusterctlConfigPath).To(BeAnExistingFile(), "Invalid argument. clusterctlConfigPath must be an existing file when calling %s spec", specName)
		Expect(bootstrapClusterProxy).NotTo(BeNil(), "Invalid argument. bootstrapClusterProxy can't be nil when calling %s spec", specName)
		Expect(os.MkdirAll(artifactFolder, 0755)).To(Succeed(), "Invalid argument. artifactFolder can't be created for %s spec", specName)
		Expect(e2eConfig.Variables).To(HaveKey(KubernetesVersionUpgradeFrom))
		Expect(e2eConfig.Variables).To(HaveKey(KubernetesVersionUpgradeTo))

		// set up a Namespace where to host objects for this spec and create a watcher for the namespace events.
		namespace, cancelWatches = setupSpecNamespace(ctx, specName, bootstrapClusterProxy, artifactFolder)
		clusterResources = new(clusterctl.ApplyClusterTemplateAndWaitResult)
		hosts = newByoHosts(ctx)
	})

	It("Should successfully upgrade cluster", func() {
		clusterName := fmt.Sprintf("%s-%s", specName, util.RandomString(6))
		kubernetesVersionUpgradeFrom := e2eConfig.MustGetVariable(KubernetesVersionUpgradeFrom)
		kubernetesVersionUpgradeTo := e2eConfig.MustGetVariable(KubernetesVersionUpgradeTo)

		// The control plane rolls out with maxSurge 1 (the variable
		// controlPlaneMaxSurge), so it needs a spare control plane host for the
		// new machine. The workers roll out with maxSurge 0: the new machine
		// reuses the host of the old one after the agent has reset it.
		By("Creating byohost capacity pool containing 2 control plane hosts and 1 worker host")
		hosts.Start(ctx, namespace.Name, []byoHost{
			{Name: fmt.Sprintf("byohost-%s", util.RandomString(6)), Role: controlPlaneRole},
			{Name: fmt.Sprintf("byohost-%s", util.RandomString(6)), Role: controlPlaneRole},
			{Name: fmt.Sprintf("byohost-%s", util.RandomString(6)), Role: workerRole},
		})

		By("creating a workload cluster with one control plane node and one worker node")
		clusterctl.ApplyClusterTemplateAndWait(ctx, clusterctl.ApplyClusterTemplateAndWaitInput{
			ClusterProxy: bootstrapClusterProxy,
			ConfigCluster: clusterctl.ConfigClusterInput{
				LogFolder:                filepath.Join(artifactFolder, "clusters", bootstrapClusterProxy.GetName()),
				ClusterctlConfigPath:     clusterctlConfigPath,
				KubeconfigPath:           bootstrapClusterProxy.GetKubeconfigPath(),
				InfrastructureProvider:   clusterctl.DefaultInfrastructureProvider,
				Flavor:                   "topology",
				Namespace:                namespace.Name,
				ClusterName:              clusterName,
				KubernetesVersion:        kubernetesVersionUpgradeFrom,
				ControlPlaneMachineCount: ptr.To[int64](1),
				WorkerMachineCount:       ptr.To[int64](1),
			},
			WaitForClusterIntervals:      e2eConfig.GetIntervals(specName, "wait-cluster"),
			WaitForControlPlaneIntervals: e2eConfig.GetIntervals(specName, "wait-control-plane"),
			WaitForMachineDeployments:    e2eConfig.GetIntervals(specName, "wait-worker-nodes"),
		}, clusterResources)

		// The ClusterClass has no variables for the etcd and CoreDNS image
		// tags, so they are left empty: kubeadm upgrades them to its defaults
		// for the new version, and the framework does not wait for particular
		// tags.
		By("Upgrading the cluster topology")
		framework.UpgradeClusterTopologyAndWaitForUpgrade(ctx, framework.UpgradeClusterTopologyAndWaitForUpgradeInput{
			ClusterProxy:                         bootstrapClusterProxy,
			Cluster:                              clusterResources.Cluster,
			ControlPlane:                         clusterResources.ControlPlane,
			MachineDeployments:                   clusterResources.MachineDeployments,
			KubernetesUpgradeVersion:             kubernetesVersionUpgradeTo,
			WaitForControlPlaneToBeUpgraded:      e2eConfig.GetIntervals(specName, "wait-machine-upgrade"),
			WaitForMachineDeploymentToBeUpgraded: e2eConfig.GetIntervals(specName, "wait-machine-upgrade"),
			WaitForKubeProxyUpgrade:              e2eConfig.GetIntervals(specName, "wait-machine-upgrade"),
		})

		By("Waiting until nodes are ready")
		workloadProxy := bootstrapClusterProxy.GetWorkloadCluster(ctx, namespace.Name, clusterResources.Cluster.Name)
		workloadClient := workloadProxy.GetClient()
		framework.WaitForNodesReady(ctx, framework.WaitForNodesReadyInput{
			Lister:            workloadClient,
			KubernetesVersion: kubernetesVersionUpgradeTo,
			Count:             int(clusterResources.ExpectedTotalNodes()),
			WaitForNodesReady: e2eConfig.GetIntervals(specName, "wait-nodes-ready"),
		})
	})

	JustAfterEach(func() {
		if CurrentSpecReport().Failed() {
			ShowInfo(hosts.LogFiles())
		}
	})

	AfterEach(func() {
		// Dumps all the resources in the spec namespace, then cleanups the cluster object and the spec namespace itself.
		// The hosts and the load balancer are removed after this.
		dumpSpecResourcesAndCleanup(ctx, specName, bootstrapClusterProxy, artifactFolder, namespace, cancelWatches, clusterResources.Cluster, e2eConfig.GetIntervals, skipCleanup)

		err := os.Remove(ReadByohControllerManagerLogShellFile)
		if err != nil {
			Showf("error removing file %s: %v", ReadByohControllerManagerLogShellFile, err)
		}
		err = os.Remove(ReadAllPodsShellFile)
		if err != nil {
			Showf("error removing file %s: %v", ReadAllPodsShellFile, err)
		}
	})
})
