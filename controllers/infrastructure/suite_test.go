// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"context"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	infrastructurev1beta1 "github.com/superbrothers/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	controllers "github.com/superbrothers/cluster-api-provider-bringyourownhost/controllers/infrastructure"
	"github.com/superbrothers/cluster-api-provider-bringyourownhost/test/builder"
	"github.com/superbrothers/cluster-api-provider-bringyourownhost/test/utils/crds"

	//+kubebuilder:scaffold:imports

	fakeclientset "k8s.io/client-go/kubernetes/fake"
	bootstrapv1 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/controllers/clustercache"
	ctrl "sigs.k8s.io/controller-runtime"
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var (
	testEnv                               *envtest.Environment
	clientFake                            client.Client
	clientSetFake                         = fakeclientset.NewSimpleClientset()
	reconciler                            *controllers.ByoMachineReconciler
	byoClusterReconciler                  *controllers.ByoClusterReconciler
	byoAdmissionReconciler                *controllers.ByoAdmissionReconciler
	k8sInstallerConfigReconciler          *controllers.K8sInstallerConfigReconciler
	bootstrapKubeconfigReconciler         *controllers.BootstrapKubeconfigReconciler
	recorder                              *events.FakeRecorder
	byoCluster                            *infrastructurev1beta1.ByoCluster
	capiCluster                           *clusterv1.Cluster
	defaultClusterName                    = "my-cluster"
	defaultNodeName                       = "my-host"
	defaultByoHostName                    = "my-host"
	defaultMachineName                    = "my-machine"
	defaultByoMachineName                 = "my-byomachine"
	defaultK8sInstallerConfigName         = "my-k8sinstallerconfig"
	defaultK8sInstallerConfigTemplateName = "my-installer-template"
	defaultNamespace                      = "default"
	fakeBootstrapSecret                   = "fakeBootstrapSecret"
	k8sManager                            ctrl.Manager
	cfg                                   *rest.Config
	ctx                                   context.Context
	cancel                                context.CancelFunc
)

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecsWithDefaultAndCustomReporters(t,
		"Controller Suite",
		[]Reporter{})
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
	ctx, cancel = context.WithCancel(context.TODO())

	By("bootstrapping test environment")
	capiCoreCRDs, err := crds.ClusterAPICoreDir()
	Expect(err).NotTo(HaveOccurred())
	capiKubeadmBootstrapCRDs, err := crds.ClusterAPIKubeadmBootstrapDir()
	Expect(err).NotTo(HaveOccurred())
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
			capiCoreCRDs,
			capiKubeadmBootstrapCRDs,
		},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	err = infrastructurev1beta1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = clusterv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = bootstrapv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	//+kubebuilder:scaffold:scheme

	k8sManager, err = ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme.Scheme,
		Metrics: metricsserver.Options{BindAddress: ":6080"},
	})
	Expect(err).NotTo(HaveOccurred())

	byoCluster = builder.ByoCluster(defaultNamespace, defaultClusterName).Build()
	Expect(k8sManager.GetClient().Create(context.Background(), byoCluster)).Should(Succeed())

	capiCluster = builder.Cluster(defaultNamespace, defaultClusterName).WithInfrastructureRef(byoCluster).Build()
	Expect(k8sManager.GetClient().Create(context.Background(), capiCluster)).Should(Succeed())

	node := builder.Node(defaultNamespace, defaultNodeName).Build()
	clientFake = fake.NewClientBuilder().WithObjects(
		capiCluster,
		node,
	).Build()

	recorder = events.NewFakeRecorder(32)
	reconciler = &controllers.ByoMachineReconciler{
		Client:       k8sManager.GetClient(),
		ClusterCache: clustercache.NewFakeClusterCache(clientFake, client.ObjectKey{Name: capiCluster.Name, Namespace: capiCluster.Namespace}),
		Recorder:     recorder,
	}
	err = reconciler.SetupWithManager(context.TODO(), k8sManager)
	Expect(err).NotTo(HaveOccurred())

	byoClusterReconciler = &controllers.ByoClusterReconciler{
		Client: k8sManager.GetClient(),
	}
	err = byoClusterReconciler.SetupWithManager(context.TODO(), k8sManager)
	Expect(err).NotTo(HaveOccurred())

	byoAdmissionReconciler = &controllers.ByoAdmissionReconciler{
		ClientSet: clientSetFake,
	}
	err = byoAdmissionReconciler.SetupWithManager(k8sManager)
	Expect(err).NotTo(HaveOccurred())

	k8sInstallerConfigReconciler = &controllers.K8sInstallerConfigReconciler{
		Client: k8sManager.GetClient(),
	}
	err = k8sInstallerConfigReconciler.SetupWithManager(k8sManager)
	Expect(err).NotTo(HaveOccurred())

	bootstrapKubeconfigReconciler = &controllers.BootstrapKubeconfigReconciler{
		Client: k8sManager.GetClient(),
	}
	err = bootstrapKubeconfigReconciler.SetupWithManager(k8sManager)
	Expect(err).NotTo(HaveOccurred())

	go func() {
		err = k8sManager.GetCache().Start(ctx)
		Expect(err).NotTo(HaveOccurred())
	}()

	Expect(k8sManager.GetCache().WaitForCacheSync(context.TODO())).To(BeTrue())

})

var _ = AfterSuite(func() {
	cancel()
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})

func WaitForObjectsToBePopulatedInCache(objects ...client.Object) {
	for _, object := range objects {
		objectCopy := object.DeepCopyObject().(client.Object)
		key := client.ObjectKeyFromObject(object)
		Eventually(func() (done bool) {
			if err := reconciler.Client.Get(context.TODO(), key, objectCopy); err != nil {
				return false
			}
			return true
		}).Should(BeTrue())
	}
}

func WaitForObjectToBeUpdatedInCache(object client.Object, testObjectUpdatedFunc func(client.Object) bool) {
	objectCopy := object.DeepCopyObject().(client.Object)
	key := client.ObjectKeyFromObject(object)
	Eventually(func() (done bool) {
		if err := reconciler.Client.Get(context.TODO(), key, objectCopy); err != nil {
			return false
		}
		if testObjectUpdatedFunc(objectCopy) {
			return true
		}
		return false
	}).Should(BeTrue())
}
