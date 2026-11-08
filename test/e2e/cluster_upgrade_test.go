// Copyright 2022 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/test/framework"
	"sigs.k8s.io/cluster-api/test/framework/clusterctl"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Cluster upgrade test without spare hosts [K8s-upgrade]", func() {

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

		// There are as many hosts as machines. Both the KubeadmControlPlane
		// (CONTROL_PLANE_MAX_SURGE, which can be 0 only with 3 or more
		// machines) and the MachineDeployment (the template) roll out with
		// maxSurge 0: they delete an old machine first, and the new machine
		// reuses the host that the agent has reset. The ClusterClass upgrade
		// spec covers the control plane with maxSurge 1 and a spare host.
		By("Creating byohost capacity pool containing 3 control plane hosts and 1 worker host")
		pool := []byoHost{{Name: fmt.Sprintf("byohost-%s", util.RandomString(6)), Role: workerRole}}
		for range 3 {
			pool = append(pool, byoHost{Name: fmt.Sprintf("byohost-%s", util.RandomString(6)), Role: controlPlaneRole})
		}
		hosts.Start(ctx, namespace.Name, pool)
		var hostNames []string
		for _, host := range pool {
			hostNames = append(hostNames, host.Name)
		}

		By("creating a workload cluster with three control plane nodes and one worker node")
		clusterctl.ApplyClusterTemplateAndWait(ctx, clusterctl.ApplyClusterTemplateAndWaitInput{
			ClusterProxy: bootstrapClusterProxy,
			ConfigCluster: clusterctl.ConfigClusterInput{
				LogFolder:                filepath.Join(artifactFolder, "clusters", bootstrapClusterProxy.GetName()),
				ClusterctlConfigPath:     clusterctlConfigPath,
				KubeconfigPath:           bootstrapClusterProxy.GetKubeconfigPath(),
				InfrastructureProvider:   clusterctl.DefaultInfrastructureProvider,
				Flavor:                   clusterctl.DefaultFlavor,
				Namespace:                namespace.Name,
				ClusterName:              clusterName,
				KubernetesVersion:        kubernetesVersionUpgradeFrom,
				ControlPlaneMachineCount: ptr.To[int64](3),
				WorkerMachineCount:       ptr.To[int64](1),
				ClusterctlVariables:      map[string]string{"CONTROL_PLANE_MAX_SURGE": "0"},
			},
			WaitForClusterIntervals:      e2eConfig.GetIntervals(specName, "wait-cluster"),
			WaitForControlPlaneIntervals: e2eConfig.GetIntervals(specName, "wait-control-plane"),
			WaitForMachineDeployments:    e2eConfig.GetIntervals(specName, "wait-worker-nodes"),
		}, clusterResources)
		Expect(clusterResources.ControlPlane.Spec.Rollout.Strategy.RollingUpdate.MaxSurge).To(HaveValue(Equal(intstr.FromInt32(0))))

		// Record the machines, to check that the upgrade replaces all of them.
		// The nodes are named after the hosts.
		oldMachines := machineNodes(ctx, clusterResources.Cluster)
		Expect(slices.Collect(maps.Values(oldMachines))).To(ConsistOf(hostNames))

		// The etcd and CoreDNS image tags are left empty, so that kubeadm
		// upgrades them to its defaults for the new version, and the framework
		// does not wait for particular tags.
		By("Upgrading the control plane")
		framework.UpgradeControlPlaneAndWaitForUpgrade(ctx, framework.UpgradeControlPlaneAndWaitForUpgradeInput{
			ClusterProxy:                bootstrapClusterProxy,
			Cluster:                     clusterResources.Cluster,
			ControlPlane:                clusterResources.ControlPlane,
			KubernetesUpgradeVersion:    kubernetesVersionUpgradeTo,
			WaitForMachinesToBeUpgraded: e2eConfig.GetIntervals(specName, "wait-machine-upgrade"),
			WaitForKubeProxyUpgrade:     e2eConfig.GetIntervals(specName, "wait-machine-upgrade"),
		})

		By("Upgrading the machine deployment")
		framework.UpgradeMachineDeploymentsAndWait(ctx, framework.UpgradeMachineDeploymentsAndWaitInput{
			ClusterProxy:                bootstrapClusterProxy,
			Cluster:                     clusterResources.Cluster,
			UpgradeVersion:              kubernetesVersionUpgradeTo,
			MachineDeployments:          clusterResources.MachineDeployments,
			WaitForMachinesToBeUpgraded: e2eConfig.GetIntervals(specName, "wait-machine-upgrade"),
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

		// The control plane runs with one machine fewer during the rollout, so
		// check that it and etcd have all their members again.
		framework.WaitForControlPlaneToBeReady(ctx, framework.WaitForControlPlaneToBeReadyInput{
			Getter:       bootstrapClusterProxy.GetClient(),
			ControlPlane: clusterResources.ControlPlane,
		}, e2eConfig.GetIntervals(specName, "wait-control-plane")...)

		By("Waiting until etcd has a healthy member on each control plane machine")
		Eventually(func(g Gomega) {
			kcp := &controlplanev1.KubeadmControlPlane{}
			g.Expect(bootstrapClusterProxy.GetClient().Get(ctx, client.ObjectKeyFromObject(clusterResources.ControlPlane), kcp)).To(Succeed())
			g.Expect(conditions.IsTrue(kcp, controlplanev1.KubeadmControlPlaneEtcdClusterHealthyCondition)).To(BeTrue(),
				"the condition %s of the KubeadmControlPlane is not true", controlplanev1.KubeadmControlPlaneEtcdClusterHealthyCondition)
		}, e2eConfig.GetIntervals(specName, "wait-control-plane")...).Should(Succeed())

		// There are no spare hosts, so the new machines are on the hosts of the
		// old ones.
		By("Checking that the new machines reuse the hosts of the old ones")
		newMachines := machineNodes(ctx, clusterResources.Cluster)
		for name := range newMachines {
			Expect(oldMachines).NotTo(HaveKey(name), "the machine %s has not been replaced", name)
		}
		Expect(slices.Collect(maps.Values(newMachines))).To(ConsistOf(hostNames))
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

// machineNodes returns the names of the nodes of the machines of the cluster
// by the names of the machines.
func machineNodes(ctx context.Context, cluster *clusterv1.Cluster) map[string]string {
	machines := framework.GetMachinesByCluster(ctx, framework.GetMachinesByClusterInput{
		Lister:      bootstrapClusterProxy.GetClient(),
		ClusterName: cluster.Name,
		Namespace:   cluster.Namespace,
	})
	nodes := make(map[string]string, len(machines))
	for i := range machines {
		m := &machines[i]
		Expect(m.Status.NodeRef.IsDefined()).To(BeTrue(), "the machine %s has no node", m.Name)
		nodes[m.Name] = m.Status.NodeRef.Name
	}
	return nodes
}
