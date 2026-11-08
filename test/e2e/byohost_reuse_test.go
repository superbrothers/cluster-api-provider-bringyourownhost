// Copyright 2021 VMware, Inc. All Rights Reserved.
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
	infrastructurev1beta1 "github.com/superbrothers/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	corev1 "k8s.io/api/core/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/test/framework"
	"sigs.k8s.io/cluster-api/test/framework/clusterctl"
	"sigs.k8s.io/cluster-api/util"
)

var _ = Describe("When BYO Host rejoins the capacity pool", func() {

	var (
		ctx              context.Context
		specName         = "byohost-reuse"
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

		Expect(e2eConfig.Variables).To(HaveKey(KubernetesVersion))

		// set up a Namespace where to host objects for this spec and create a watcher for the namespace events.
		namespace, cancelWatches = setupSpecNamespace(ctx, specName, bootstrapClusterProxy, artifactFolder)
		clusterResources = new(clusterctl.ApplyClusterTemplateAndWaitResult)
		hosts = newByoHosts(ctx)
	})

	It("Should reuse the same BYO Host after it is reset", func() {
		clusterName := fmt.Sprintf("%s-%s", specName, util.RandomString(6))
		// The hosts have random names, as the CSR of a host is named after it
		// and is left in the management cluster for the later specs.
		byoHostName1 := fmt.Sprintf("byohost-%s", util.RandomString(6))
		byoHostName2 := fmt.Sprintf("byohost-%s", util.RandomString(6))

		// Both clusters use the same hosts and the same load balancer.
		hosts.Start(ctx, namespace.Name, []byoHost{
			{Name: byoHostName1, Role: controlPlaneRole},
			{Name: byoHostName2, Role: workerRole},
		})

		By("Creating a cluster")

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
				KubernetesVersion:        e2eConfig.MustGetVariable(KubernetesVersion),
				ControlPlaneMachineCount: ptr.To[int64](1),
				WorkerMachineCount:       ptr.To[int64](1),
			},
			WaitForClusterIntervals:      e2eConfig.GetIntervals(specName, "wait-cluster"),
			WaitForControlPlaneIntervals: e2eConfig.GetIntervals(specName, "wait-control-plane"),
			WaitForMachineDeployments:    e2eConfig.GetIntervals(specName, "wait-worker-nodes"),
		}, clusterResources)

		byoHostLookupKey := k8stypes.NamespacedName{Name: byoHostName2, Namespace: namespace.Name}
		expectByoHostAttachedToCluster(ctx, byoHostLookupKey, clusterName)

		By("Delete the cluster and freeing the ByoHosts")
		framework.DeleteAllClustersAndWait(ctx, framework.DeleteAllClustersAndWaitInput{
			ClusterProxy:         bootstrapClusterProxy,
			ClusterctlConfigPath: clusterctlConfigPath,
			Namespace:            namespace.Name,
			ArtifactFolder:       artifactFolder,
		}, e2eConfig.GetIntervals(specName, "wait-delete-cluster")...)

		// The deletion of a Machine waits until the agent resets its host, and
		// the controller detaches the host, so the host has rejoined the
		// capacity pool when the cluster is deleted.
		freedByoHost := &infrastructurev1beta1.ByoHost{}
		Expect(bootstrapClusterProxy.GetClient().Get(ctx, byoHostLookupKey, freedByoHost)).To(Succeed())
		Expect(freedByoHost.Spec).To(Equal(infrastructurev1beta1.ByoHostSpec{}))
		Expect(freedByoHost.Status.LastReset).NotTo(BeNil())

		By("Creating a new cluster")
		clusterName = fmt.Sprintf("%s-%s", specName, util.RandomString(6))

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
				KubernetesVersion:        e2eConfig.MustGetVariable(KubernetesVersion),
				ControlPlaneMachineCount: ptr.To[int64](1),
				WorkerMachineCount:       ptr.To[int64](1),
			},
			WaitForClusterIntervals:      e2eConfig.GetIntervals(specName, "wait-cluster"),
			WaitForControlPlaneIntervals: e2eConfig.GetIntervals(specName, "wait-control-plane"),
			WaitForMachineDeployments:    e2eConfig.GetIntervals(specName, "wait-worker-nodes"),
		}, clusterResources)

		expectByoHostAttachedToCluster(ctx, byoHostLookupKey, clusterName)

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
			Showf("Failed to remove file %s: %v", ReadByohControllerManagerLogShellFile, err)
		}
		err = os.Remove(ReadAllPodsShellFile)
		if err != nil {
			Showf("Failed to remove file %s: %v", ReadAllPodsShellFile, err)
		}
	})
})

// expectByoHostAttachedToCluster expects that the ByoHost is attached to a
// ByoMachine of the cluster.
func expectByoHostAttachedToCluster(ctx context.Context, byoHostLookupKey k8stypes.NamespacedName, clusterName string) {
	byoHost := &infrastructurev1beta1.ByoHost{}
	Expect(bootstrapClusterProxy.GetClient().Get(ctx, byoHostLookupKey, byoHost)).To(Succeed())
	Expect(byoHost.Spec.ByoMachineRef).NotTo(BeNil(), "ByoHost %s is not attached to a ByoMachine", byoHostLookupKey)

	byoMachine := &infrastructurev1beta1.ByoMachine{}
	byoMachineLookupKey := k8stypes.NamespacedName{Name: byoHost.Spec.ByoMachineRef.Name, Namespace: byoHostLookupKey.Namespace}
	Expect(bootstrapClusterProxy.GetClient().Get(ctx, byoMachineLookupKey, byoMachine)).To(Succeed())
	Expect(byoMachine.UID).To(Equal(byoHost.Spec.ByoMachineRef.UID))
	Expect(byoMachine.Labels).To(HaveKeyWithValue(clusterv1.ClusterNameLabel, clusterName))
}
