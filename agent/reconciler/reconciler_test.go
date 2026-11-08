// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package reconciler_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/superbrothers/cluster-api-provider-bringyourownhost/agent/cloudinit/cloudinitfakes"
	"github.com/superbrothers/cluster-api-provider-bringyourownhost/agent/reconciler"
	infrastructurev1beta1 "github.com/superbrothers/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/superbrothers/cluster-api-provider-bringyourownhost/test/builder"
	eventutils "github.com/superbrothers/cluster-api-provider-bringyourownhost/test/utils/events"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/core/v1beta1"
	v1beta1conditions "sigs.k8s.io/cluster-api/util/deprecated/v1beta1/conditions"
	v1beta1patch "sigs.k8s.io/cluster-api/util/deprecated/v1beta1/patch"

	controllerruntime "sigs.k8s.io/controller-runtime"
)

var _ = Describe("Byohost Agent Tests", func() {

	var (
		ctx                 = context.TODO()
		ns                  = "default"
		hostName            = "test-host"
		byoHost             *infrastructurev1beta1.ByoHost
		byoMachine          *infrastructurev1beta1.ByoMachine
		byoHostLookupKey    types.NamespacedName
		bootstrapSecret     *corev1.Secret
		installationSecret  *corev1.Secret
		recorder            *events.FakeRecorder
		uninstallScript     string
		uninstallScriptPath string
	)

	BeforeEach(func() {
		fakeCommandRunner = &cloudinitfakes.FakeICmdRunner{}
		fakeFileWriter = &cloudinitfakes.FakeIFileWriter{}
		fakeTemplateParser = &cloudinitfakes.FakeITemplateParser{}
		recorder = events.NewFakeRecorder(32)
		uninstallScriptPath = filepath.Join(GinkgoT().TempDir(), "uninstall.sh")
		hostReconciler = &reconciler.HostReconciler{
			Client:              k8sClient,
			CmdRunner:           fakeCommandRunner,
			FileWriter:          fakeFileWriter,
			TemplateParser:      fakeTemplateParser,
			Recorder:            recorder,
			SkipK8sInstallation: false,
			UninstallScriptPath: uninstallScriptPath,
		}
	})

	It("should do nothing if ByoHost is not found", func() {
		// An administrator has deleted the ByoHost.
		_, err := hostReconciler.Reconcile(ctx, controllerruntime.Request{
			NamespacedName: types.NamespacedName{
				Name:      "non-existent-host",
				Namespace: ns},
		})
		Expect(err).NotTo(HaveOccurred())
	})

	Context("When ByoHost exists", func() {
		BeforeEach(func() {
			byoHost = builder.ByoHost(ns, hostName).Build()
			Expect(k8sClient.Create(ctx, byoHost)).NotTo(HaveOccurred(), "failed to create byohost")
			var err error
			patchHelper, err = v1beta1patch.NewHelper(byoHost, k8sClient)
			Expect(err).ShouldNot(HaveOccurred())

			byoHostLookupKey = types.NamespacedName{Name: byoHost.Name, Namespace: ns}
		})

		It("should set the Reason to WaitingForByoMachineRefReason if the ByoHost is not attached", func() {
			result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
				NamespacedName: byoHostLookupKey,
			})

			Expect(result).To(Equal(controllerruntime.Result{}))
			Expect(reconcilerErr).ToNot(HaveOccurred())

			updatedByoHost := &infrastructurev1beta1.ByoHost{}
			err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
			Expect(err).ToNot(HaveOccurred())
			k8sNodeBootstrapSucceeded := v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
			Expect(*k8sNodeBootstrapSucceeded).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
				Type:     infrastructurev1beta1.K8sNodeBootstrapSucceeded,
				Status:   corev1.ConditionFalse,
				Reason:   infrastructurev1beta1.WaitingForByoMachineRefReason,
				Severity: clusterv1beta1.ConditionSeverityInfo,
			}))
		})

		It("should mark a ByoHost that is no longer attached as free", func() {
			// The conditions of the earlier attachment.
			v1beta1conditions.MarkTrue(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
			v1beta1conditions.MarkTrue(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)
			Expect(patchHelper.Patch(ctx, byoHost, v1beta1patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())

			_, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
				NamespacedName: byoHostLookupKey,
			})
			Expect(reconcilerErr).ToNot(HaveOccurred())
			Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(0))

			updatedByoHost := &infrastructurev1beta1.ByoHost{}
			Expect(k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)).To(Succeed())
			Expect(*v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
				Type:     infrastructurev1beta1.K8sNodeBootstrapSucceeded,
				Status:   corev1.ConditionFalse,
				Reason:   infrastructurev1beta1.WaitingForByoMachineRefReason,
				Severity: clusterv1beta1.ConditionSeverityInfo,
			}))
			Expect(v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)).To(BeNil())
		})

		Context("When the ByoHost is attached to a ByoMachine", func() {
			BeforeEach(func() {
				byoMachine = builder.ByoMachine(ns, "test-byomachine").Build()
				Expect(k8sClient.Create(ctx, byoMachine)).NotTo(HaveOccurred(), "failed to create byomachine")
				byoHost.Spec.ByoMachineRef = &infrastructurev1beta1.ByoMachineReference{
					Name: byoMachine.Name,
					UID:  byoMachine.UID,
				}
				Expect(patchHelper.Patch(ctx, byoHost, v1beta1patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())
			})

			It("should set the Reason to BootstrapDataSecretUnavailableReason", func() {
				result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
					NamespacedName: byoHostLookupKey,
				})
				Expect(result).To(Equal(controllerruntime.Result{}))
				Expect(reconcilerErr).ToNot(HaveOccurred())

				updatedByoHost := &infrastructurev1beta1.ByoHost{}
				err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
				Expect(err).ToNot(HaveOccurred())

				byoHostRegistrationSucceeded := v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
				Expect(*byoHostRegistrationSucceeded).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
					Type:     infrastructurev1beta1.K8sNodeBootstrapSucceeded,
					Status:   corev1.ConditionFalse,
					Reason:   infrastructurev1beta1.BootstrapDataSecretUnavailableReason,
					Severity: clusterv1beta1.ConditionSeverityInfo,
				}))
			})

			It("should return an error if we fail to load the bootstrap secret", func() {
				byoHost.Spec.BootstrapSecret = &corev1.ObjectReference{
					Kind:      "Secret",
					Namespace: "non-existent",
					Name:      "non-existent",
				}
				Expect(patchHelper.Patch(ctx, byoHost, v1beta1patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())

				result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
					NamespacedName: byoHostLookupKey,
				})
				Expect(result).To(Equal(controllerruntime.Result{}))
				Expect(reconcilerErr).To(MatchError("secrets \"non-existent\" not found"))

				// assert events
				events := eventutils.CollectEvents(recorder.Events)
				Expect(events).Should(ConsistOf([]string{
					fmt.Sprintf("Warning ReadBootstrapSecretFailed bootstrap secret %s not found", byoHost.Spec.BootstrapSecret.Name),
				}))
			})

			Context("When bootstrap secret is ready", func() {
				BeforeEach(func() {
					secretData := `write_files:
- path: fake/path
  content: blah
runCmd:
- echo 'run some command'`

					bootstrapSecret = builder.Secret(ns, "test-secret").
						WithData(secretData).
						Build()
					Expect(k8sClient.Create(ctx, bootstrapSecret)).NotTo(HaveOccurred())

					byoHost.Spec.BootstrapSecret = &corev1.ObjectReference{
						Kind:      "Secret",
						Namespace: bootstrapSecret.Namespace,
						Name:      bootstrapSecret.Name,
					}

					Expect(patchHelper.Patch(ctx, byoHost, v1beta1patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())
				})

				It("should skip k8s installation if skip-installation is set", func() {
					hostReconciler.SkipK8sInstallation = true
					result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
						NamespacedName: byoHostLookupKey,
					})
					Expect(result).To(Equal(controllerruntime.Result{}))
					Expect(reconcilerErr).ToNot(HaveOccurred())

					updatedByoHost := &infrastructurev1beta1.ByoHost{}
					err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
					Expect(err).ToNot(HaveOccurred())

					k8sNodeBootstrapSucceeded := v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
					Expect(*k8sNodeBootstrapSucceeded).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
						Type:   infrastructurev1beta1.K8sNodeBootstrapSucceeded,
						Status: corev1.ConditionTrue,
					}))

					// assert events
					events := eventutils.CollectEvents(recorder.Events)
					Expect(events).ShouldNot(ContainElement(
						"Normal k8sComponentInstalled Successfully Installed K8s components",
					))
				})

				It("should set the Reason to InstallationSecretUnavailableReason", func() {
					result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
						NamespacedName: byoHostLookupKey,
					})
					Expect(result).To(Equal(controllerruntime.Result{}))
					Expect(reconcilerErr).ToNot(HaveOccurred())

					updatedByoHost := &infrastructurev1beta1.ByoHost{}
					err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
					Expect(err).ToNot(HaveOccurred())

					byoHostRegistrationSucceeded := v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)
					Expect(*byoHostRegistrationSucceeded).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
						Type:     infrastructurev1beta1.K8sComponentsInstallationSucceeded,
						Status:   corev1.ConditionFalse,
						Reason:   infrastructurev1beta1.K8sInstallationSecretUnavailableReason,
						Severity: clusterv1beta1.ConditionSeverityInfo,
					}))
				})

				It("should return an error if we fail to load the installation secret", func() {
					byoHost.Spec.InstallationSecret = &corev1.ObjectReference{
						Kind:      "Secret",
						Namespace: "non-existent",
						Name:      "non-existent",
					}
					Expect(patchHelper.Patch(ctx, byoHost, v1beta1patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())

					result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
						NamespacedName: byoHostLookupKey,
					})
					Expect(result).To(Equal(controllerruntime.Result{}))
					Expect(reconcilerErr).To(MatchError("secrets \"non-existent\" not found"))

					// assert events
					events := eventutils.CollectEvents(recorder.Events)
					Expect(events).Should(ConsistOf([]string{
						fmt.Sprintf("Warning ReadInstallationSecretFailed install and uninstall script %s not found", byoHost.Spec.InstallationSecret.Name),
					}))
				})

				Context("When installation secret is ready", func() {
					var installScript string

					BeforeEach(func() {
						// The scripts contain "{{" to check that they are run as they are.
						installScript = `echo "{{install}}"`
						uninstallScript = `echo "{{uninstall}}"`

						installationSecret = builder.Secret(ns, "test-secret3").
							WithKeyData("install", installScript).
							WithKeyData("uninstall", uninstallScript).
							Build()
						Expect(k8sClient.Create(ctx, installationSecret)).NotTo(HaveOccurred())

						byoHost.Spec.InstallationSecret = &corev1.ObjectReference{
							Kind:      "Secret",
							Namespace: installationSecret.Namespace,
							Name:      installationSecret.Name,
						}

						Expect(patchHelper.Patch(ctx, byoHost, v1beta1patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())
					})

					It("should execute bootstrap secret only once ", func() {

						_, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(reconcilerErr).ToNot(HaveOccurred())

						_, reconcilerErr = hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(reconcilerErr).ToNot(HaveOccurred())

						Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(2)) // one cmd call is for install script
						Expect(fakeFileWriter.WriteToFileCallCount()).To(Equal(1))
					})

					It("should set K8sNodeBootstrapSucceeded to True if the boostrap execution succeeds", func() {

						result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(result).To(Equal(controllerruntime.Result{}))
						Expect(reconcilerErr).ToNot(HaveOccurred())

						Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(2)) // one cmd call is for install script
						Expect(fakeFileWriter.WriteToFileCallCount()).To(Equal(1))

						updatedByoHost := &infrastructurev1beta1.ByoHost{}
						err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
						Expect(err).ToNot(HaveOccurred())

						k8sNodeBootstrapSucceeded := v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
						Expect(*k8sNodeBootstrapSucceeded).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
							Type:   infrastructurev1beta1.K8sNodeBootstrapSucceeded,
							Status: corev1.ConditionTrue,
						}))

						// assert events
						events := eventutils.CollectEvents(recorder.Events)
						Expect(events).Should(ConsistOf([]string{
							"Normal InstallScriptExecutionSucceeded install script executed",
							"Normal BootstrapK8sNodeSucceeded k8s Node Bootstraped",
						}))
					})

					It("should set K8sNodeBootstrapSucceeded to false with Reason CloudInitExecutionFailedReason if the bootstrap execution fails", func() {
						v1beta1conditions.MarkTrue(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)
						Expect(patchHelper.Patch(ctx, byoHost, v1beta1patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())

						fakeCommandRunner.RunCmdReturns(errors.New("I failed"))

						result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})

						Expect(result).To(Equal(controllerruntime.Result{}))
						Expect(reconcilerErr).To(HaveOccurred())

						updatedByoHost := &infrastructurev1beta1.ByoHost{}
						err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
						Expect(err).ToNot(HaveOccurred())

						k8sNodeBootstrapSucceeded := v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
						Expect(*k8sNodeBootstrapSucceeded).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
							Type:     infrastructurev1beta1.K8sNodeBootstrapSucceeded,
							Status:   corev1.ConditionFalse,
							Reason:   infrastructurev1beta1.CloudInitExecutionFailedReason,
							Severity: clusterv1beta1.ConditionSeverityError,
						}))

						// assert events
						events := eventutils.CollectEvents(recorder.Events)
						Expect(events).Should(ConsistOf([]string{
							"Warning BootstrapK8sNodeFailed k8s Node Bootstrap failed",
							// TODO: improve test to remove this event
							"Warning ResetK8sNodeFailed k8s Node Reset failed",
						}))
					})

					It("should return error if install script execution failed", func() {
						fakeCommandRunner.RunCmdReturns(errors.New("failed to execute install script"))
						invalidInstallationSecret := builder.Secret(ns, "invalid-test-secret").
							WithKeyData("install", "test").
							Build()
						Expect(k8sClient.Create(ctx, invalidInstallationSecret)).NotTo(HaveOccurred())
						byoHost.Spec.InstallationSecret = &corev1.ObjectReference{
							Kind:      "Secret",
							Namespace: invalidInstallationSecret.Namespace,
							Name:      invalidInstallationSecret.Name,
						}
						Expect(patchHelper.Patch(ctx, byoHost, v1beta1patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())

						result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(result).To(Equal(controllerruntime.Result{}))
						Expect(reconcilerErr).To(HaveOccurred())

						// assert events
						events := eventutils.CollectEvents(recorder.Events)
						Expect(events).Should(ConsistOf([]string{
							"Warning InstallScriptExecutionFailed install script execution failed",
						}))
					})

					It("should return error if installation secrent does not exists", func() {
						fakeCommandRunner.RunCmdReturns(errors.New("failed to execute install script"))
						byoHost.Spec.InstallationSecret = &corev1.ObjectReference{
							Kind:      "Secret",
							Namespace: "non-existent",
							Name:      "non-existent",
						}
						Expect(patchHelper.Patch(ctx, byoHost, v1beta1patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())

						result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(result).To(Equal(controllerruntime.Result{}))
						Expect(reconcilerErr).To(HaveOccurred())

						// assert events
						events := eventutils.CollectEvents(recorder.Events)
						Expect(events).Should(ConsistOf([]string{
							"Warning ReadInstallationSecretFailed install and uninstall script non-existent not found",
						}))

					})

					It("should execute the install script as it is", func() {
						_, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(reconcilerErr).ToNot(HaveOccurred())

						_, installCommand := fakeCommandRunner.RunCmdArgsForCall(0)
						Expect(installCommand).To(Equal(installScript))
					})

					It("should keep the uninstall script in a file only readable by the agent, and leave the spec as it is", func() {
						result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(result).To(Equal(controllerruntime.Result{}))
						Expect(reconcilerErr).NotTo(HaveOccurred())

						Expect(os.ReadFile(uninstallScriptPath)).To(Equal([]byte(uninstallScript)))
						info, err := os.Stat(uninstallScriptPath)
						Expect(err).NotTo(HaveOccurred())
						Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))

						updatedByoHost := &infrastructurev1beta1.ByoHost{}
						Expect(k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)).To(Succeed())
						Expect(updatedByoHost.Spec).To(Equal(byoHost.Spec))
					})

					It("should not keep the uninstall script if the install script fails", func() {
						fakeCommandRunner.RunCmdReturns(errors.New("failed to execute install script"))

						_, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(reconcilerErr).To(HaveOccurred())
						Expect(uninstallScriptPath).NotTo(BeAnExistingFile())
					})

					It("should set K8sComponentsInstallationSucceeded to true if Install succeeds", func() {
						result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(result).To(Equal(controllerruntime.Result{}))
						Expect(reconcilerErr).ToNot(HaveOccurred())

						updatedByoHost := &infrastructurev1beta1.ByoHost{}
						err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
						Expect(err).ToNot(HaveOccurred())

						K8sComponentsInstallationSucceeded := v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)
						Expect(*K8sComponentsInstallationSucceeded).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
							Type:   infrastructurev1beta1.K8sComponentsInstallationSucceeded,
							Status: corev1.ConditionTrue,
						}))

						// assert events
						events := eventutils.CollectEvents(recorder.Events)
						Expect(events).Should(ConsistOf([]string{
							"Normal InstallScriptExecutionSucceeded install script executed",
							"Normal BootstrapK8sNodeSucceeded k8s Node Bootstraped",
						}))
					})

					It("should set K8sNodeBootstrapSucceeded to True if the boostrap execution succeeds", func() {
						result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(result).To(Equal(controllerruntime.Result{}))
						Expect(reconcilerErr).ToNot(HaveOccurred())

						Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(2))
						Expect(fakeFileWriter.WriteToFileCallCount()).To(Equal(1))

						updatedByoHost := &infrastructurev1beta1.ByoHost{}
						err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
						Expect(err).ToNot(HaveOccurred())

						k8sNodeBootstrapSucceeded := v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
						Expect(*k8sNodeBootstrapSucceeded).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
							Type:   infrastructurev1beta1.K8sNodeBootstrapSucceeded,
							Status: corev1.ConditionTrue,
						}))

						// assert events
						events := eventutils.CollectEvents(recorder.Events)
						Expect(events).Should(ConsistOf([]string{
							"Normal InstallScriptExecutionSucceeded install script executed",
							"Normal BootstrapK8sNodeSucceeded k8s Node Bootstraped",
						}))
					})
					AfterEach(func() {
						Expect(k8sClient.Delete(ctx, installationSecret)).NotTo(HaveOccurred())
					})
				})

				AfterEach(func() {
					Expect(k8sClient.Delete(ctx, bootstrapSecret)).NotTo(HaveOccurred())
					hostReconciler.SkipK8sInstallation = false
				})
			})

			AfterEach(func() {
				Expect(k8sClient.Delete(ctx, byoMachine)).NotTo(HaveOccurred())
			})
		})

		Context("When a reset of the host is requested", func() {
			BeforeEach(func() {
				// The script contains "{{" to check that it is run as it is.
				uninstallScript = `echo "{{uninstall success script}}"`
				byoMachine = builder.ByoMachine(ns, "test-byomachine").Build()
				Expect(k8sClient.Create(ctx, byoMachine)).NotTo(HaveOccurred(), "failed to create byomachine")
				byoHost.Spec.ByoMachineRef = &infrastructurev1beta1.ByoMachineReference{
					Name: byoMachine.Name,
					UID:  byoMachine.UID,
				}
				byoHost.Spec.ResetRequested = true
				v1beta1conditions.MarkTrue(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
				v1beta1conditions.MarkTrue(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)
				Expect(patchHelper.Patch(ctx, byoHost, v1beta1patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())
			})

			AfterEach(func() {
				Expect(k8sClient.Delete(ctx, byoMachine)).NotTo(HaveOccurred())
			})

			Context("When the agent has installed the k8s components", func() {
				BeforeEach(func() {
					Expect(os.WriteFile(uninstallScriptPath, []byte(uninstallScript), 0o600)).To(Succeed())
				})

				It("should reset the node, uninstall the k8s components, and report the reset", func() {
					result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
						NamespacedName: byoHostLookupKey,
					})
					Expect(result).To(Equal(controllerruntime.Result{}))
					Expect(reconcilerErr).ToNot(HaveOccurred())

					Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(2))
					_, resetCommand := fakeCommandRunner.RunCmdArgsForCall(0)
					Expect(resetCommand).To(Equal(reconciler.KubeadmResetCommand))
					_, uninstallCommand := fakeCommandRunner.RunCmdArgsForCall(1)
					Expect(uninstallCommand).To(Equal(uninstallScript))
					Expect(uninstallScriptPath).NotTo(BeAnExistingFile())

					updatedByoHost := &infrastructurev1beta1.ByoHost{}
					Expect(k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)).To(Succeed())
					// The agent leaves the spec to the controller.
					Expect(updatedByoHost.Spec).To(Equal(byoHost.Spec))
					Expect(updatedByoHost.Status.LastReset).NotTo(BeNil())
					Expect(updatedByoHost.Status.LastReset.ByoMachineUID).To(Equal(byoMachine.UID))
					Expect(updatedByoHost.Status.LastReset.Time.IsZero()).To(BeFalse())

					Expect(*v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
						Type:     infrastructurev1beta1.K8sNodeBootstrapSucceeded,
						Status:   corev1.ConditionFalse,
						Reason:   infrastructurev1beta1.K8sNodeAbsentReason,
						Severity: clusterv1beta1.ConditionSeverityInfo,
					}))
					Expect(*v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
						Type:     infrastructurev1beta1.K8sComponentsInstallationSucceeded,
						Status:   corev1.ConditionFalse,
						Reason:   infrastructurev1beta1.K8sNodeAbsentReason,
						Severity: clusterv1beta1.ConditionSeverityInfo,
					}))

					events := eventutils.CollectEvents(recorder.Events)
					Expect(events).Should(ConsistOf([]string{
						"Normal ResetK8sNodeSucceeded k8s Node Reset completed",
						fmt.Sprintf("Normal ResetHostSucceeded Host reset for ByoMachine %s", byoMachine.Name),
					}))
				})

				It("should not reset the host again once it has reported the reset", func() {
					_, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
						NamespacedName: byoHostLookupKey,
					})
					Expect(reconcilerErr).ToNot(HaveOccurred())
					Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(2))

					_, reconcilerErr = hostReconciler.Reconcile(ctx, controllerruntime.Request{
						NamespacedName: byoHostLookupKey,
					})
					Expect(reconcilerErr).ToNot(HaveOccurred())
					Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(2))
				})

				It("should report the failure of kubeadm reset, and not report the reset", func() {
					fakeCommandRunner.RunCmdReturnsOnCall(0, errors.New("failed to reset"))

					_, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
						NamespacedName: byoHostLookupKey,
					})
					Expect(reconcilerErr).To(MatchError("failed to exec kubeadm reset: failed to reset"))
					Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(1))
					Expect(uninstallScriptPath).To(BeAnExistingFile())

					updatedByoHost := &infrastructurev1beta1.ByoHost{}
					Expect(k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)).To(Succeed())
					Expect(updatedByoHost.Status.LastReset).To(BeNil())
					Expect(*v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
						Type:     infrastructurev1beta1.K8sNodeBootstrapSucceeded,
						Status:   corev1.ConditionFalse,
						Reason:   infrastructurev1beta1.K8sNodeResetFailedReason,
						Severity: clusterv1beta1.ConditionSeverityWarning,
						Message:  "failed to exec kubeadm reset: failed to reset",
					}))

					events := eventutils.CollectEvents(recorder.Events)
					Expect(events).Should(ConsistOf([]string{
						"Warning ResetK8sNodeFailed k8s Node Reset failed",
					}))
				})

				It("should report the failure of the uninstall script, keep the script, and not report the reset", func() {
					fakeCommandRunner.RunCmdReturnsOnCall(1, errors.New("failed to uninstall"))

					_, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
						NamespacedName: byoHostLookupKey,
					})
					Expect(reconcilerErr).To(MatchError("failed to uninstall"))
					// The agent runs the script again when it tries again.
					Expect(uninstallScriptPath).To(BeAnExistingFile())

					updatedByoHost := &infrastructurev1beta1.ByoHost{}
					Expect(k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)).To(Succeed())
					Expect(updatedByoHost.Status.LastReset).To(BeNil())
					Expect(*v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
						Type:     infrastructurev1beta1.K8sComponentsInstallationSucceeded,
						Status:   corev1.ConditionFalse,
						Reason:   infrastructurev1beta1.K8sComponentsUninstallationFailedReason,
						Severity: clusterv1beta1.ConditionSeverityWarning,
						Message:  "failed to uninstall",
					}))

					events := eventutils.CollectEvents(recorder.Events)
					Expect(events).Should(ConsistOf([]string{
						"Normal ResetK8sNodeSucceeded k8s Node Reset completed",
						"Warning UninstallScriptExecutionFailed uninstall script execution failed",
					}))
				})
			})

			It("should skip the reset of the node and the uninstallation if the agent has not installed the k8s components", func() {
				_, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
					NamespacedName: byoHostLookupKey,
				})
				Expect(reconcilerErr).ToNot(HaveOccurred())
				Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(0))

				updatedByoHost := &infrastructurev1beta1.ByoHost{}
				Expect(k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)).To(Succeed())
				Expect(updatedByoHost.Status.LastReset.ByoMachineUID).To(Equal(byoMachine.UID))
			})

			It("should reset the node and skip the uninstallation if skip-installation is set", func() {
				hostReconciler.SkipK8sInstallation = true

				_, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
					NamespacedName: byoHostLookupKey,
				})
				Expect(reconcilerErr).ToNot(HaveOccurred())
				Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(1))
				_, resetCommand := fakeCommandRunner.RunCmdArgsForCall(0)
				Expect(resetCommand).To(Equal(reconciler.KubeadmResetCommand))

				updatedByoHost := &infrastructurev1beta1.ByoHost{}
				Expect(k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)).To(Succeed())
				Expect(updatedByoHost.Status.LastReset.ByoMachineUID).To(Equal(byoMachine.UID))
				Expect(*v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
					Type:     infrastructurev1beta1.K8sNodeBootstrapSucceeded,
					Status:   corev1.ConditionFalse,
					Reason:   infrastructurev1beta1.K8sNodeAbsentReason,
					Severity: clusterv1beta1.ConditionSeverityInfo,
				}))
			})

			It("should reset the host for the current attachment even if it has reported a reset for an earlier one", func() {
				byoHost.Status.LastReset = &infrastructurev1beta1.LastReset{
					ByoMachineUID: "uid-of-an-earlier-byomachine",
					Time:          metav1.Now(),
				}
				Expect(patchHelper.Patch(ctx, byoHost, v1beta1patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())
				hostReconciler.SkipK8sInstallation = true

				_, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
					NamespacedName: byoHostLookupKey,
				})
				Expect(reconcilerErr).ToNot(HaveOccurred())
				Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(1))

				updatedByoHost := &infrastructurev1beta1.ByoHost{}
				Expect(k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)).To(Succeed())
				Expect(updatedByoHost.Status.LastReset.ByoMachineUID).To(Equal(byoMachine.UID))
			})
		})

		AfterEach(func() {
			Expect(k8sClient.Delete(ctx, byoHost)).NotTo(HaveOccurred())
			hostReconciler.SkipK8sInstallation = false
		})
	})
})
