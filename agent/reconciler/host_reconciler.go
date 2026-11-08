// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"fmt"
	"os"

	"github.com/pkg/errors"
	"github.com/superbrothers/cluster-api-provider-bringyourownhost/agent/cloudinit"
	"github.com/superbrothers/cluster-api-provider-bringyourownhost/common"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/core/v1beta1"
	v1beta1conditions "sigs.k8s.io/cluster-api/util/deprecated/v1beta1/conditions"
	v1beta1patch "sigs.k8s.io/cluster-api/util/deprecated/v1beta1/patch"
	"sigs.k8s.io/cluster-api/util/predicates"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	infrastructurev1beta1 "github.com/superbrothers/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
)

// HostReconciler encapsulates the data/logic needed to reconcile a ByoHost.
// It reads the spec of the ByoHost, which the controller writes, and writes
// only the status.
type HostReconciler struct {
	Client              client.Client
	CmdRunner           cloudinit.ICmdRunner
	FileWriter          cloudinit.IFileWriter
	TemplateParser      cloudinit.ITemplateParser
	Recorder            events.EventRecorder
	SkipK8sInstallation bool
	// UninstallScriptPath is the file where the agent keeps the uninstall
	// script of the Kubernetes components that it has installed, until it
	// resets the host. The file exists only while the components that the
	// agent has installed are on the host.
	UninstallScriptPath string
}

const (
	bootstrapSentinelFile = "/run/cluster-api/bootstrap-success.complete"
	// KubeadmResetCommand is the command to run to force reset/remove nodes' local file system of the files created by kubeadm
	KubeadmResetCommand = "kubeadm reset --force"
	// DefaultUninstallScriptPath is the default UninstallScriptPath of the
	// agent, in its working directory (/var/lib/byoh-hostagent with the
	// package).
	DefaultUninstallScriptPath = "uninstall.sh"
	// uninstallScriptMode is the mode of the file of the uninstall script,
	// which only the agent reads.
	uninstallScriptMode = 0o600
)

// Reconcile handles events for the ByoHost that is registered by this agent process
func (r *HostReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	logger := ctrl.LoggerFrom(ctx)
	logger.Info("Reconcile request received")

	// Fetch the ByoHost instance
	byoHost := &infrastructurev1beta1.ByoHost{}
	err := r.Client.Get(ctx, req.NamespacedName, byoHost)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// An administrator has deleted the ByoHost. The agent registers
			// the host again when it starts.
			logger.Info("ByoHost not found")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "error getting ByoHost")
		return ctrl.Result{}, err
	}
	helper, _ := v1beta1patch.NewHelper(byoHost, r.Client)
	defer func() {
		err = helper.Patch(ctx, byoHost)
		if err != nil && reterr == nil {
			logger.Error(err, "failed to patch byohost")
			reterr = err
		}
	}()

	switch {
	case byoHost.Spec.ByoMachineRef == nil:
		r.reconcileFree(ctx, byoHost)
		return ctrl.Result{}, nil
	case byoHost.Spec.ResetRequested:
		return ctrl.Result{}, r.reconcileReset(ctx, byoHost)
	default:
		return r.reconcileNormal(ctx, byoHost)
	}
}

// reconcileFree marks the ByoHost as free while it is not attached to a
// ByoMachine. The conditions of an earlier attachment would otherwise make the
// agent skip the installation and the bootstrap for the next one.
func (r *HostReconciler) reconcileFree(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) {
	ctrl.LoggerFrom(ctx).Info("ByoHost is not attached to a ByoMachine")
	v1beta1conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded, infrastructurev1beta1.WaitingForByoMachineRefReason, clusterv1beta1.ConditionSeverityInfo, "")
	v1beta1conditions.Delete(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)
}

func (r *HostReconciler) reconcileNormal(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx)
	logger = logger.WithValues("ByoHost", byoHost.Name)
	logger.Info("reconcile normal")

	if byoHost.Spec.BootstrapSecret == nil {
		logger.Info("BootstrapDataSecret not ready")
		v1beta1conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded, infrastructurev1beta1.BootstrapDataSecretUnavailableReason, clusterv1beta1.ConditionSeverityInfo, "")
		return ctrl.Result{}, nil
	}

	if !v1beta1conditions.IsTrue(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded) {
		bootstrapScript, err := r.getBootstrapScript(ctx, byoHost.Spec.BootstrapSecret.Name, byoHost.Spec.BootstrapSecret.Namespace)
		if err != nil {
			logger.Error(err, "error getting bootstrap script")
			r.Recorder.Eventf(byoHost, nil, corev1.EventTypeWarning, "ReadBootstrapSecretFailed", "BootstrapK8sNode", "bootstrap secret %s not found", byoHost.Spec.BootstrapSecret.Name)
			return ctrl.Result{}, err
		}

		if r.SkipK8sInstallation {
			logger.Info("Skipping installation of k8s components")
		} else if !v1beta1conditions.IsTrue(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded) {
			if byoHost.Spec.InstallationSecret == nil {
				logger.Info("InstallationSecret not ready")
				v1beta1conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded, infrastructurev1beta1.K8sInstallationSecretUnavailableReason, clusterv1beta1.ConditionSeverityInfo, "")
				return ctrl.Result{}, nil
			}
			err = r.executeInstallerController(ctx, byoHost)
			if err != nil {
				return ctrl.Result{}, err
			}
			r.Recorder.Eventf(byoHost, nil, corev1.EventTypeNormal, "InstallScriptExecutionSucceeded", "InstallK8sComponents", "install script executed")
			v1beta1conditions.MarkTrue(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)
		} else {
			logger.Info("install script already executed")
		}

		err = r.cleank8sdirectories(ctx)
		if err != nil {
			logger.Error(err, "error cleaning up k8s directories, please delete it manually for reconcile to proceed.")
			r.Recorder.Eventf(byoHost, nil, corev1.EventTypeWarning, "CleanK8sDirectoriesFailed", "BootstrapK8sNode", "clean k8s directories failed")
			v1beta1conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded, infrastructurev1beta1.CleanK8sDirectoriesFailedReason, clusterv1beta1.ConditionSeverityError, "")
			return ctrl.Result{}, err
		}

		err = r.bootstrapK8sNode(ctx, bootstrapScript, byoHost)
		if err != nil {
			logger.Error(err, "error in bootstrapping k8s node")
			r.Recorder.Eventf(byoHost, nil, corev1.EventTypeWarning, "BootstrapK8sNodeFailed", "BootstrapK8sNode", "k8s Node Bootstrap failed")
			_ = r.resetNode(ctx, byoHost)
			v1beta1conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded, infrastructurev1beta1.CloudInitExecutionFailedReason, clusterv1beta1.ConditionSeverityError, "")
			return ctrl.Result{}, err
		}
		logger.Info("k8s node successfully bootstrapped")
		r.Recorder.Eventf(byoHost, nil, corev1.EventTypeNormal, "BootstrapK8sNodeSucceeded", "BootstrapK8sNode", "k8s Node Bootstraped")
		v1beta1conditions.MarkTrue(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
	}

	return ctrl.Result{}, nil
}

// executeInstallerController runs the install script, and keeps the uninstall
// script for the reset of the host.
func (r *HostReconciler) executeInstallerController(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) error {
	logger := ctrl.LoggerFrom(ctx)
	secret := &corev1.Secret{}
	err := r.Client.Get(ctx, types.NamespacedName{Name: byoHost.Spec.InstallationSecret.Name, Namespace: byoHost.Spec.InstallationSecret.Namespace}, secret)
	if err != nil {
		logger.Error(err, "error getting install and uninstall script")
		r.Recorder.Eventf(byoHost, nil, corev1.EventTypeWarning, "ReadInstallationSecretFailed", "InstallK8sComponents", "install and uninstall script %s not found", byoHost.Spec.InstallationSecret.Name)
		return err
	}
	installScript := string(secret.Data["install"])
	uninstallScript := string(secret.Data["uninstall"])

	logger.Info("executing install script")
	err = r.CmdRunner.RunCmd(ctx, installScript)
	if err != nil {
		logger.Error(err, "error executing installation script")
		r.Recorder.Eventf(byoHost, nil, corev1.EventTypeWarning, "InstallScriptExecutionFailed", "InstallK8sComponents", "install script execution failed")
		v1beta1conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded, infrastructurev1beta1.K8sComponentsInstallationFailedReason, clusterv1beta1.ConditionSeverityInfo, "")
		return err
	}

	// Keep the uninstall script on the host: the agent writes only the status
	// of the ByoHost, and the installation Secret may be gone when the agent
	// resets the host.
	if err := r.saveUninstallScript(uninstallScript); err != nil {
		logger.Error(err, "error keeping the uninstall script")
		r.Recorder.Eventf(byoHost, nil, corev1.EventTypeWarning, "SaveUninstallScriptFailed", "InstallK8sComponents", "keeping the uninstall script failed")
		return err
	}
	return nil
}

// reconcileReset resets the host for spec.resetRequested, and reports it in
// status.lastReset with the UID of the ByoMachine, unless the agent has
// already reset the host for the current attachment.
func (r *HostReconciler) reconcileReset(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) error {
	logger := ctrl.LoggerFrom(ctx).WithValues("ByoHost", byoHost.Name, "ByoMachine", byoHost.Spec.ByoMachineRef.Name)
	uid := byoHost.Spec.ByoMachineRef.UID
	if byoHost.Status.LastReset != nil && byoHost.Status.LastReset.ByoMachineUID == uid {
		logger.Info("Host already reset, waiting for the controller to detach the ByoHost")
		return nil
	}

	if err := r.resetHost(ctx, byoHost); err != nil {
		return err
	}
	byoHost.Status.LastReset = &infrastructurev1beta1.LastReset{
		ByoMachineUID: uid,
		Time:          metav1.Now(),
	}
	logger.Info("Host reset")
	r.Recorder.Eventf(byoHost, nil, corev1.EventTypeNormal, "ResetHostSucceeded", "ResetHost", "Host reset for ByoMachine %s", byoHost.Spec.ByoMachineRef.Name)
	return nil
}

// resetHost resets the host, so that it can be attached again: it runs
// kubeadm reset, uninstalls the Kubernetes components that the agent has
// installed, and removes the bootstrap sentinel file. kubeadm reset runs when
// kubeadm is on the host: the agent has installed the components, or
// --skip-installation says that the host has them.
func (r *HostReconciler) resetHost(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) error {
	logger := ctrl.LoggerFrom(ctx)
	logger.Info("Resetting host")

	uninstallScript, installed, err := r.readUninstallScript()
	if err != nil {
		logger.Error(err, "error reading the uninstall script")
		r.Recorder.Eventf(byoHost, nil, corev1.EventTypeWarning, "ReadUninstallScriptFailed", "UninstallK8sComponents", "reading the uninstall script failed")
		v1beta1conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded, infrastructurev1beta1.K8sComponentsUninstallationFailedReason, clusterv1beta1.ConditionSeverityWarning, "%v", err)
		return err
	}

	if installed || r.SkipK8sInstallation {
		if err := r.resetNode(ctx, byoHost); err != nil {
			v1beta1conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded, infrastructurev1beta1.K8sNodeResetFailedReason, clusterv1beta1.ConditionSeverityWarning, "%v", err)
			return err
		}
	} else {
		logger.Info("Skipping kubeadm reset, as the agent has not installed the k8s components")
	}
	v1beta1conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded, infrastructurev1beta1.K8sNodeAbsentReason, clusterv1beta1.ConditionSeverityInfo, "")

	if installed {
		logger.Info("Executing Uninstall script")
		if err := r.CmdRunner.RunCmd(ctx, uninstallScript); err != nil {
			logger.Error(err, "error execting Uninstallation script")
			r.Recorder.Eventf(byoHost, nil, corev1.EventTypeWarning, "UninstallScriptExecutionFailed", "UninstallK8sComponents", "uninstall script execution failed")
			v1beta1conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded, infrastructurev1beta1.K8sComponentsUninstallationFailedReason, clusterv1beta1.ConditionSeverityWarning, "%v", err)
			return err
		}
		if err := os.Remove(r.UninstallScriptPath); err != nil && !os.IsNotExist(err) {
			v1beta1conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded, infrastructurev1beta1.K8sComponentsUninstallationFailedReason, clusterv1beta1.ConditionSeverityWarning, "%v", err)
			return errors.Wrapf(err, "failed to remove the uninstall script %s", r.UninstallScriptPath)
		}
	} else if !r.SkipK8sInstallation {
		logger.Info("Skipping k8s component uninstallation, as the agent has not installed them")
	}
	v1beta1conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded, infrastructurev1beta1.K8sNodeAbsentReason, clusterv1beta1.ConditionSeverityInfo, "")

	if err := r.removeSentinelFile(ctx, byoHost); err != nil {
		v1beta1conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded, infrastructurev1beta1.K8sNodeResetFailedReason, clusterv1beta1.ConditionSeverityWarning, "%v", err)
		return err
	}
	return nil
}

// saveUninstallScript keeps the uninstall script in UninstallScriptPath. It
// writes a temporary file first, so that the file never has a part of the
// script.
func (r *HostReconciler) saveUninstallScript(script string) error {
	tmp := r.UninstallScriptPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(script), uninstallScriptMode); err != nil {
		return errors.Wrapf(err, "failed to write the uninstall script to %s", tmp)
	}
	return errors.Wrapf(os.Rename(tmp, r.UninstallScriptPath), "failed to move the uninstall script to %s", r.UninstallScriptPath)
}

// readUninstallScript returns the uninstall script in UninstallScriptPath,
// and whether there is one.
func (r *HostReconciler) readUninstallScript() (script string, found bool, err error) {
	data, err := os.ReadFile(r.UninstallScriptPath)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, errors.Wrapf(err, "failed to read the uninstall script %s", r.UninstallScriptPath)
	}
	return string(data), true, nil
}

func (r *HostReconciler) getBootstrapScript(ctx context.Context, dataSecretName, namespace string) (string, error) {
	secret := &corev1.Secret{}
	err := r.Client.Get(ctx, types.NamespacedName{Name: dataSecretName, Namespace: namespace}, secret)
	if err != nil {
		return "", err
	}

	bootstrapSecret := string(secret.Data["value"])
	return bootstrapSecret, nil
}

// SetupWithManager sets up the controller with the manager
func (r *HostReconciler) SetupWithManager(ctx context.Context, mgr manager.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrastructurev1beta1.ByoHost{}).
		WithEventFilter(predicates.ResourceNotPaused(mgr.GetScheme(), ctrl.LoggerFrom(ctx))).
		Complete(r)
}

// cleanup /run/kubeadm, /etc/cni/net.d dirs to remove any stale config on the host
func (r *HostReconciler) cleank8sdirectories(ctx context.Context) error {
	logger := ctrl.LoggerFrom(ctx)

	dirs := []string{
		"/run/kubeadm/*",
		"/etc/cni/net.d/*",
	}

	errList := make([]error, 0)
	for _, dir := range dirs {
		logger.Info(fmt.Sprintf("cleaning up directory %s", dir))
		if err := common.RemoveGlob(dir); err != nil {
			logger.Error(err, fmt.Sprintf("failed to clean up directory %s", dir))
			errList = append(errList, err)
		}
	}

	if len(errList) > 0 {
		err := errList[0]               //nolint: gosec
		for _, e := range errList[1:] { //nolint: gosec
			err = fmt.Errorf("%w; %v error", err, e)
		}
		return errors.WithMessage(err, "not all k8s directories are cleaned up")
	}
	return nil
}

func (r *HostReconciler) resetNode(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) error {
	logger := ctrl.LoggerFrom(ctx)
	logger.Info("Running kubeadm reset")

	err := r.CmdRunner.RunCmd(ctx, KubeadmResetCommand)
	if err != nil {
		r.Recorder.Eventf(byoHost, nil, corev1.EventTypeWarning, "ResetK8sNodeFailed", "ResetK8sNode", "k8s Node Reset failed")
		return errors.Wrapf(err, "failed to exec kubeadm reset")
	}
	logger.Info("Kubernetes Node reset completed")
	r.Recorder.Eventf(byoHost, nil, corev1.EventTypeNormal, "ResetK8sNodeSucceeded", "ResetK8sNode", "k8s Node Reset completed")
	return nil
}

func (r *HostReconciler) bootstrapK8sNode(ctx context.Context, bootstrapScript string, byoHost *infrastructurev1beta1.ByoHost) error {
	logger := ctrl.LoggerFrom(ctx)
	logger.Info("Bootstraping k8s Node")
	return cloudinit.ScriptExecutor{
		WriteFilesExecutor:    r.FileWriter,
		RunCmdExecutor:        r.CmdRunner,
		ParseTemplateExecutor: r.TemplateParser}.Execute(bootstrapScript)
}

func (r *HostReconciler) removeSentinelFile(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) error {
	logger := ctrl.LoggerFrom(ctx)
	logger.Info("Removing the bootstrap sentinel file")
	if _, err := os.Stat(bootstrapSentinelFile); !os.IsNotExist(err) {
		err := os.Remove(bootstrapSentinelFile)
		if err != nil {
			return errors.Wrapf(err, "failed to delete sentinel file %s", bootstrapSentinelFile)
		}
	}
	return nil
}
