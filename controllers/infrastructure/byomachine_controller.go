// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/cluster-api/controllers/clustercache"
	"sigs.k8s.io/cluster-api/controllers/external"
	"sigs.k8s.io/cluster-api/util"
	v1beta1conditions "sigs.k8s.io/cluster-api/util/deprecated/v1beta1/conditions"
	v1beta1patch "sigs.k8s.io/cluster-api/util/deprecated/v1beta1/patch"
	"sigs.k8s.io/cluster-api/util/predicates"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/go-logr/logr"
	infrav1 "github.com/superbrothers/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/core/v1beta1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/annotations"
)

const (
	// ProviderIDPrefix prefix for provider id
	ProviderIDPrefix = "byoh://"
	// ProviderIDSuffixLength length of provider id suffix
	ProviderIDSuffixLength = 6
	// DefaultRequeuePeriod is the default interval to check again while a
	// controller waits for a resource, such as a free ByoHost.
	DefaultRequeuePeriod = 10 * time.Second
	// claimConflictRequeueAfter is the delay to pick a ByoHost again, or to
	// detach it again, after another writer has changed the ByoHost that the
	// ByoMachine was claiming or detaching. It is short, as the ByoMachine does
	// not wait for a resource, and gives the cache time to catch up.
	claimConflictRequeueAfter = 1 * time.Second
)

// requeuePeriodOrDefault returns the requeue period, or DefaultRequeuePeriod
// when it is not set.
func requeuePeriodOrDefault(period time.Duration) time.Duration {
	if period <= 0 {
		return DefaultRequeuePeriod
	}
	return period
}

// ByoMachineReconciler reconciles a ByoMachine object
type ByoMachineReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	ClusterCache clustercache.ClusterCache
	Recorder     events.EventRecorder
	// RequeuePeriod is the interval to check again while the ByoMachine waits
	// for a free ByoHost or for the installer config. DefaultRequeuePeriod is
	// used when it is not set.
	RequeuePeriod time.Duration
	// MaxConcurrentReconciles is the number of ByoMachines to process
	// simultaneously. The default of controller-runtime (1) is used when it is
	// not set.
	MaxConcurrentReconciles int
}

//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byomachines,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byomachines/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byomachines/finalizers,verbs=update
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byohosts,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=*,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters;machines,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines;machines/status,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the ByoMachine object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.8.3/pkg/reconcile

// Reconcile handles ByoMachine events
// nolint: gocyclo, funlen
func (r *ByoMachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconcile request received")

	// Fetch the ByoMachine instance
	byoMachine := &infrav1.ByoMachine{}
	err := r.Client.Get(ctx, req.NamespacedName, byoMachine)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Fetch the Machine
	machine, err := util.GetOwnerMachine(ctx, r.Client, byoMachine.ObjectMeta)
	if err != nil {
		logger.Error(err, "failed to get Owner Machine")
		return ctrl.Result{}, err
	}

	if machine == nil {
		logger.Info("Waiting for Machine Controller to set OwnerRef on ByoMachine")
		return ctrl.Result{}, nil
	}

	// Fetch the Cluster
	cluster, err := util.GetClusterFromMetadata(ctx, r.Client, byoMachine.ObjectMeta)
	if err != nil {
		logger.Error(err, "ByoMachine owner Machine is missing cluster label or cluster does not exist")
		return ctrl.Result{}, err
	}

	if cluster == nil {
		logger.Info(fmt.Sprintf("Please associate this machine with a cluster using the label %s: <name of cluster>", clusterv1.ClusterNameLabel))
		return ctrl.Result{}, nil
	}
	logger = logger.WithValues("cluster", cluster.Name)

	byoCluster := &infrav1.ByoCluster{}
	infraClusterName := client.ObjectKey{
		Namespace: byoMachine.Namespace,
		Name:      cluster.Spec.InfrastructureRef.Name,
	}

	if err = r.Client.Get(ctx, infraClusterName, byoCluster); err != nil {
		logger.Error(err, "failed to get infra cluster")
		return ctrl.Result{}, nil
	}

	helper, _ := v1beta1patch.NewHelper(byoMachine, r.Client)
	defer func() {
		if err = helper.Patch(ctx, byoMachine); err != nil && reterr == nil {
			logger.Error(err, "failed to patch byomachine")
			reterr = err
		}
	}()

	// Fetch the BYOHost which is referencing this machine, if any
	refByoHost, err := r.FetchAttachedByoHost(ctx, byoMachine)
	if err != nil {
		return ctrl.Result{}, err
	}
	if refByoHost != nil {
		logger = logger.WithValues("BYOHost", refByoHost.Name)
	}

	// Create the machine scope
	machineScope, err := newByoMachineScope(byoMachineScopeParams{
		Client:     r.Client,
		Cluster:    cluster,
		Machine:    machine,
		ByoCluster: byoCluster,
		ByoMachine: byoMachine,
		ByoHost:    refByoHost,
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	// Return early if the object or Cluster is paused
	if annotations.IsPaused(cluster, byoMachine) {
		logger.Info("byoMachine or linked Cluster is marked as paused. Won't reconcile")
		v1beta1conditions.MarkFalse(byoMachine, infrav1.BYOHostReady, infrav1.ClusterOrResourcePausedReason, clusterv1beta1.ConditionSeverityInfo, "")
		return ctrl.Result{}, nil
	}

	// Handle deleted machines
	if !byoMachine.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, machineScope)
	}

	// Handle non-deleted machines
	return r.reconcileNormal(ctx, machineScope)
}

// FetchAttachedByoHost returns the ByoHost attached to the ByoMachine, or nil
// when there is none. The ByoHost refers to the ByoMachine in
// spec.byoMachineRef by its name and UID. A ByoHost that refers to an earlier
// ByoMachine of the same name, which is left when an administrator removes the
// finalizer of a ByoMachine, is not attached to this one.
func (r *ByoMachineReconciler) FetchAttachedByoHost(ctx context.Context, byoMachine *infrav1.ByoMachine) (*infrav1.ByoHost, error) {
	logger := log.FromContext(ctx)

	hostsList := &infrav1.ByoHostList{}
	// A ByoMachine only takes a ByoHost in its own namespace (see attachByoHost).
	err := r.Client.List(
		ctx,
		hostsList,
		client.InNamespace(byoMachine.Namespace),
		client.MatchingFields{byoHostByoMachineNameField: byoMachine.Name},
	)
	if err != nil {
		return nil, err
	}
	var attached *infrav1.ByoHost
	for i := range hostsList.Items {
		host := &hostsList.Items[i]
		if host.Spec.ByoMachineRef == nil || host.Spec.ByoMachineRef.UID != byoMachine.UID {
			logger.Info("Ignoring a ByoHost that is attached to an earlier ByoMachine of the same name", "byohost", host.Name)
			continue
		}
		if attached != nil {
			logger.Info("Ignoring a ByoHost, as more than one ByoHost is attached to the ByoMachine", "byohost", host.Name, "attachedByoHost", attached.Name)
			continue
		}
		attached = host
	}
	return attached, nil
}

// reconcileDelete detaches the ByoHost from the ByoMachine after the agent has
// reset the host, so that the host can be attached again. The ByoMachine, and
// so its Machine, is not deleted until then.
func (r *ByoMachineReconciler) reconcileDelete(ctx context.Context, machineScope *byoMachineScope) (reconcile.Result, error) {
	logger := log.FromContext(ctx).WithValues("cluster", machineScope.Cluster.Name)
	logger.Info("Deleting ByoMachine")

	host := machineScope.ByoHost
	if host == nil {
		controllerutil.RemoveFinalizer(machineScope.ByoMachine, infrav1.MachineFinalizer)
		return reconcile.Result{}, nil
	}

	if isHostReset(host) {
		logger.Info("Detaching the ByoHost, which the agent has reset", "byohost", host.Name)
		res, err := r.detachByoHost(ctx, host)
		if err != nil || !res.IsZero() {
			return res, err
		}
		r.Recorder.Eventf(host, machineScope.ByoMachine, corev1.EventTypeNormal, "ByoHostReleaseSucceeded", "ReleaseByoHost", "ByoHost Released by %s", machineScope.ByoMachine.Name)
		r.Recorder.Eventf(machineScope.ByoMachine, host, corev1.EventTypeNormal, "ByoHostReleaseSucceeded", "ReleaseByoHost", "Released ByoHost %s", host.Name)
		controllerutil.RemoveFinalizer(machineScope.ByoMachine, infrav1.MachineFinalizer)
		return reconcile.Result{}, nil
	}

	if !host.Spec.ResetRequested {
		logger.Info("Requesting the agent to reset the ByoHost", "byohost", host.Name)
		base := host.DeepCopy()
		host.Spec.ResetRequested = true
		if err := r.Client.Patch(ctx, host, client.MergeFrom(base)); err != nil {
			logger.Error(err, "failed to request the reset of the byohost", "byohost", host.Name)
			return ctrl.Result{}, err
		}
		r.Recorder.Eventf(host, machineScope.ByoMachine, corev1.EventTypeNormal, "ByoHostResetRequested", "ResetByoHost", "Reset requested by ByoMachine %s", machineScope.ByoMachine.Name)
		r.Recorder.Eventf(machineScope.ByoMachine, host, corev1.EventTypeNormal, "ByoHostResetRequested", "ResetByoHost", "Requested the reset of ByoHost %s", host.Name)
	}

	// The ByoHost watch reconciles the ByoMachine again when the agent reports
	// the reset.
	logger.Info("Waiting for the agent to reset the ByoHost", "byohost", host.Name)
	v1beta1conditions.MarkFalse(machineScope.ByoMachine, infrav1.BYOHostReady, infrav1.WaitingForHostResetReason, clusterv1beta1.ConditionSeverityInfo, "")
	return reconcile.Result{}, nil
}

// isHostReset returns whether the agent has reset the host for its current
// attachment. The UID tells the attachments apart, so that a report for an
// earlier ByoMachine does not detach the host.
func isHostReset(host *infrav1.ByoHost) bool {
	return host.Spec.ByoMachineRef != nil &&
		host.Status.LastReset != nil &&
		host.Status.LastReset.ByoMachineUID == host.Spec.ByoMachineRef.UID
}

// detachByoHost clears the fields of the attachment in the spec of the ByoHost,
// which returns the host to the free hosts. It uses optimistic locking, so that
// it never clears an attachment that it has not read.
func (r *ByoMachineReconciler) detachByoHost(ctx context.Context, host *infrav1.ByoHost) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	base := host.DeepCopy()
	host.Spec.ByoMachineRef = nil
	host.Spec.ResetRequested = false
	host.Spec.BootstrapSecret = nil
	host.Spec.InstallationSecret = nil
	err := r.Client.Patch(ctx, host, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	if apierrors.IsConflict(err) {
		logger.Info("ByoHost changed while detaching it, trying again", "byohost", host.Name)
		return ctrl.Result{RequeueAfter: claimConflictRequeueAfter}, nil
	}
	if err != nil {
		logger.Error(err, "failed to detach the byohost", "byohost", host.Name)
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *ByoMachineReconciler) reconcileNormal(ctx context.Context, machineScope *byoMachineScope) (reconcile.Result, error) {
	logger := log.FromContext(ctx).WithValues("cluster", machineScope.Cluster.Name)
	logger.Info("Reconciling ByoMachine")

	controllerutil.AddFinalizer(machineScope.ByoMachine, infrav1.MachineFinalizer)

	if machineScope.ByoMachine.Spec.InstallerRef != nil {
		if err := r.createInstallerConfig(ctx, machineScope); err != nil {
			logger.Error(err, "create installer config failed")
			return ctrl.Result{}, err
		}
	}

	if !ptr.Deref(machineScope.Cluster.Status.Initialization.InfrastructureProvisioned, false) {
		logger.Info("Cluster infrastructure is not ready yet")
		v1beta1conditions.MarkFalse(machineScope.ByoMachine, infrav1.BYOHostReady, infrav1.WaitingForClusterInfrastructureReason, clusterv1beta1.ConditionSeverityInfo, "")
		return reconcile.Result{}, nil
	}

	if machineScope.Machine.Spec.Bootstrap.DataSecretName == nil {
		logger.Info("Bootstrap Data Secret not available yet")
		v1beta1conditions.MarkFalse(machineScope.ByoMachine, infrav1.BYOHostReady, infrav1.WaitingForBootstrapDataSecretReason, clusterv1beta1.ConditionSeverityInfo, "")
		return reconcile.Result{}, nil
	}

	// If there is not yet an byoHost for this byoMachine,
	// then pick one from the host capacity pool
	if machineScope.ByoHost == nil {
		logger.Info("Attempting host reservation")
		res, err := r.attachByoHost(ctx, machineScope)
		// No ByoHost is attached while it waits for a free one.
		if err != nil || machineScope.ByoHost == nil {
			return res, err
		}
		v1beta1conditions.MarkFalse(machineScope.ByoMachine, infrav1.BYOHostReady, infrav1.InstallationSecretNotAvailableReason, clusterv1beta1.ConditionSeverityInfo, "")
		r.Recorder.Eventf(machineScope.ByoHost, machineScope.ByoMachine, corev1.EventTypeNormal, "ByoHostAttachSucceeded", "AttachByoHost", "Attached to ByoMachine %s", machineScope.ByoMachine.Name)
		r.Recorder.Eventf(machineScope.ByoMachine, machineScope.ByoHost, corev1.EventTypeNormal, "ByoHostAttachSucceeded", "AttachByoHost", "Attached ByoHost %s", machineScope.ByoHost.Name)
	}

	if machineScope.ByoMachine.Status.HostInfo == (infrav1.HostInfo{}) {
		machineScope.ByoMachine.Status.HostInfo = machineScope.ByoHost.Status.HostDetails
	}

	if machineScope.ByoMachine.Spec.InstallerRef != nil && machineScope.ByoHost.Spec.InstallationSecret == nil {
		res, err := r.setInstallationSecretForByoHost(ctx, machineScope)
		if err != nil {
			logger.Error(err, "failed to set installation secret on byohost")
			return res, err
		}
		if res.RequeueAfter > 0 {
			return res, nil
		}
	}

	logger.Info("Updating Node with ProviderID")
	return r.updateNodeProviderID(ctx, machineScope)
}

func (r *ByoMachineReconciler) updateNodeProviderID(ctx context.Context, machineScope *byoMachineScope) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("cluster", machineScope.Cluster.Name)
	remoteClient, err := r.getRemoteClient(ctx, machineScope.ByoMachine)
	if err != nil {
		logger.Error(err, "failed to get remote client")
		return ctrl.Result{}, err
	}

	providerID, err := r.setNodeProviderID(ctx, remoteClient, machineScope.ByoHost)
	if err != nil {
		logger.Error(err, "failed to set node providerID")
		r.Recorder.Eventf(machineScope.ByoMachine, machineScope.ByoHost, corev1.EventTypeWarning, "SetNodeProviderFailed", "SetNodeProviderID", "Node %s does not exist", machineScope.ByoHost.Name)
		return ctrl.Result{}, err
	}

	machineScope.ByoMachine.Spec.ProviderID = providerID
	machineScope.ByoMachine.Status.Ready = true
	v1beta1conditions.MarkTrue(machineScope.ByoMachine, infrav1.BYOHostReady)
	r.Recorder.Eventf(machineScope.ByoMachine, machineScope.ByoHost, corev1.EventTypeNormal, "NodeProvisionedSucceeded", "SetNodeProviderID", "Provisioned Node %s", machineScope.ByoHost.Name)
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ByoMachineReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	var (
		controlledType     = &infrav1.ByoMachine{}
		controlledTypeName = reflect.TypeOf(controlledType).Elem().Name()
		controlledTypeGVK  = infrav1.GroupVersion.WithKind(controlledTypeName)
	)
	logger := ctrl.LoggerFrom(ctx)
	ClusterToByoMachines := r.ClusterToByoMachines(logger)

	if err := mgr.GetFieldIndexer().IndexField(ctx, &infrav1.ByoHost{}, byoHostByoMachineNameField, byoHostByoMachineName); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(controlledType).
		WithOptions(controller.Options{MaxConcurrentReconciles: r.MaxConcurrentReconciles}).
		Watches(
			&infrav1.ByoHost{},
			handler.EnqueueRequestsFromMapFunc(ByoHostToByoMachine),
		).
		// Watch the CAPI resource that owns this infrastructure resource
		Watches(
			&clusterv1.Machine{},
			handler.EnqueueRequestsFromMapFunc(util.MachineToInfrastructureMapFunc(controlledTypeGVK)),
		).
		Watches(
			&clusterv1.Cluster{},
			handler.EnqueueRequestsFromMapFunc(ClusterToByoMachines),
			builder.WithPredicates(predicates.ClusterPausedTransitionsOrInfrastructureProvisioned(mgr.GetScheme(), ctrl.LoggerFrom(ctx))),
		).
		Complete(r)
}

// ClusterToByoMachines is a handler.ToRequestsFunc to be used to enqeue requests for reconciliation
// of ByoMachines
func (r *ByoMachineReconciler) ClusterToByoMachines(logger logr.Logger) handler.MapFunc {
	return func(_ context.Context, o client.Object) []ctrl.Request {
		c, ok := o.(*clusterv1.Cluster)
		if !ok {
			errMsg := fmt.Sprintf("Expected a Cluster but got a %T", o)
			logger.Error(errors.New(errMsg), errMsg)
			return nil
		}

		logger = logger.WithValues("objectMapper", "ClusterToByoMachines", "namespace", c.Namespace, "Cluster", c.Name)

		// Don't handle deleted clusters
		if !c.ObjectMeta.DeletionTimestamp.IsZero() {
			logger.Info("Cluster has a deletion timestamp, skipping mapping.")
			return nil
		}

		clusterLabels := map[string]string{clusterv1.ClusterNameLabel: c.Name}
		byoMachineList := &infrav1.ByoMachineList{}
		if err := r.Client.List(context.TODO(), byoMachineList, client.InNamespace(c.Namespace), client.MatchingLabels(clusterLabels)); err != nil {
			logger.Error(err, "Failed to get ByoMachine, skipping mapping.")
			return nil
		}

		result := make([]ctrl.Request, 0, len(byoMachineList.Items))
		for i := range byoMachineList.Items {
			logger.WithValues("byoMachine", byoMachineList.Items[i].Name)
			logger.Info("Adding ByoMachine to reconciliation request.")
			result = append(result, ctrl.Request{NamespacedName: client.ObjectKey{Namespace: byoMachineList.Items[i].Namespace, Name: byoMachineList.Items[i].Name}})
		}
		return result
	}
}

// setNodeProviderID patches the provider id to the node using
// client pointing to workload cluster
func (r *ByoMachineReconciler) setNodeProviderID(ctx context.Context, remoteClient client.Client, host *infrav1.ByoHost) (string, error) {
	node := &corev1.Node{}
	key := client.ObjectKey{Name: host.Name, Namespace: host.Namespace}
	err := remoteClient.Get(ctx, key, node)
	if err != nil {
		return "", err
	}

	if node.Spec.ProviderID != "" {
		var match bool
		match, err = regexp.MatchString(fmt.Sprintf("%s%s/.+", ProviderIDPrefix, host.Name), node.Spec.ProviderID)
		if err != nil {
			return "", err
		}
		if match {
			return node.Spec.ProviderID, nil
		}
		return "", errors.New("invalid format for node.Spec.ProviderID")
	}

	helper, err := v1beta1patch.NewHelper(node, remoteClient)
	if err != nil {
		return "", err
	}

	node.Spec.ProviderID = fmt.Sprintf("%s%s/%s", ProviderIDPrefix, host.Name, util.RandomString(ProviderIDSuffixLength))

	return node.Spec.ProviderID, helper.Patch(ctx, node)
}

func (r *ByoMachineReconciler) getRemoteClient(ctx context.Context, byoMachine *infrav1.ByoMachine) (client.Client, error) {
	cluster, err := util.GetClusterFromMetadata(ctx, r.Client, byoMachine.ObjectMeta)
	if err != nil {
		return nil, err
	}
	remoteClient, err := r.ClusterCache.GetClient(ctx, util.ObjectKey(cluster))
	if err != nil {
		return nil, err
	}

	return remoteClient, nil
}

func (r *ByoMachineReconciler) setInstallationSecretForByoHost(ctx context.Context, machineScope *byoMachineScope) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("cluster", machineScope.Cluster.Name)
	installerConfig, ready, err := r.getInstallerConfigAndStatus(ctx, machineScope)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ready {
		logger.Info("Installer config is not ready, requeuing")
		return ctrl.Result{RequeueAfter: requeuePeriodOrDefault(r.RequeuePeriod)}, nil
	}

	helper, err := v1beta1patch.NewHelper(machineScope.ByoHost, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	secret, found, err := unstructured.NestedFieldNoCopy(installerConfig.Object, "status", "installationSecret")
	if err != nil {
		return ctrl.Result{}, err
	}
	if !found {
		return ctrl.Result{}, fmt.Errorf("installation secret not set on ready installerconfig %s %s", installerConfig.GetKind(), installerConfig.GetName())
	}
	secretRef := &corev1.ObjectReference{}
	if err = runtime.DefaultUnstructuredConverter.FromUnstructured(secret.(map[string]interface{}), secretRef); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to convert unstructured field, %s", err.Error())
	}
	machineScope.ByoHost.Spec.InstallationSecret = secretRef
	return ctrl.Result{}, helper.Patch(ctx, machineScope.ByoHost)
}

func (r *ByoMachineReconciler) getInstallerConfigAndStatus(ctx context.Context, machineScope *byoMachineScope) (*unstructured.Unstructured, bool, error) {
	installerConfig, err := r.getInstallerConfig(ctx, machineScope.ByoMachine)
	if err != nil {
		return nil, false, err
	}
	ready, err := external.IsReady(installerConfig)
	if err != nil {
		return installerConfig, false, err
	}
	return installerConfig, ready, nil
}

// byoHostByoMachineNameField is the index of the ByoHosts by the name of the
// ByoMachine in spec.byoMachineRef, or "" for the free ByoHosts. The API server
// cannot select the ByoHosts by whether a field is set, so the ByoMachine
// controller looks them up in the cache with this index.
const byoHostByoMachineNameField = "spec.byoMachineRef.name"

// byoHostByoMachineName returns the value of byoHostByoMachineNameField of a
// ByoHost.
func byoHostByoMachineName(o client.Object) []string {
	host, ok := o.(*infrav1.ByoHost)
	if !ok {
		return nil
	}
	if host.Spec.ByoMachineRef == nil {
		return []string{""}
	}
	return []string{host.Spec.ByoMachineRef.Name}
}

func (r *ByoMachineReconciler) attachByoHost(ctx context.Context, machineScope *byoMachineScope) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("cluster", machineScope.Cluster.Name)
	if machineScope.ByoHost != nil {
		return ctrl.Result{}, nil
	}

	selector := labels.Everything()
	if machineScope.ByoMachine.Spec.Selector != nil {
		var err error
		selector, err = metav1.LabelSelectorAsSelector(machineScope.ByoMachine.Spec.Selector)
		if err != nil {
			logger.Error(err, "Label Selector as selector failed")
			return ctrl.Result{}, err
		}
	}

	// Take only a free ByoHost in the namespace of the ByoMachine. clusterctl
	// move carries only the objects in the namespace of the cluster, so the
	// ByoHost of a cluster must be in the same namespace to move with it.
	hostsList := &infrav1.ByoHostList{}
	err := r.Client.List(
		ctx,
		hostsList,
		client.InNamespace(machineScope.ByoMachine.Namespace),
		client.MatchingLabelsSelector{Selector: selector},
		client.MatchingFields{byoHostByoMachineNameField: ""},
	)
	if err != nil {
		logger.Error(err, "failed to list byohosts")
		return ctrl.Result{}, err
	}
	if len(hostsList.Items) == 0 {
		logger.Info("No hosts found, waiting..")
		r.Recorder.Eventf(machineScope.ByoMachine, nil, corev1.EventTypeWarning, "ByoHostSelectionFailed", "AttachByoHost", "No available ByoHost")
		v1beta1conditions.MarkFalse(machineScope.ByoMachine, infrav1.BYOHostReady, infrav1.BYOHostsUnavailableReason, clusterv1beta1.ConditionSeverityInfo, "")
		// Requeue without an error: controller-runtime ignores RequeueAfter
		// with an error, and backs off up to about 16 minutes. The ByoHost
		// events do not trigger this ByoMachine while it has no ByoHost.
		return ctrl.Result{RequeueAfter: requeuePeriodOrDefault(r.RequeuePeriod)}, nil
	}
	// TODO- Needs smarter logic
	host := hostsList.Items[0]
	base := host.DeepCopy()

	host.Spec.ByoMachineRef = &infrav1.ByoMachineReference{
		Name: machineScope.ByoMachine.Name,
		UID:  machineScope.ByoMachine.UID,
	}
	host.Spec.BootstrapSecret = &corev1.ObjectReference{
		Kind:      "Secret",
		Namespace: machineScope.ByoMachine.Namespace,
		Name:      *machineScope.Machine.Spec.Bootstrap.DataSecretName,
	}
	// Clear what an earlier attachment may have left, when an administrator
	// has detached the host by hand.
	host.Spec.InstallationSecret = nil
	host.Spec.ResetRequested = false

	// Claim the host with optimistic locking: the patch fails with a conflict
	// when the host has changed since it was read, for example when another
	// ByoMachine has claimed it. Without the lock, the last patch would win,
	// and two ByoMachines would take the same host.
	err = r.Client.Patch(ctx, &host, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	if apierrors.IsConflict(err) {
		logger.Info("ByoHost changed while claiming it, picking a host again", "byohost", host.Name)
		return ctrl.Result{RequeueAfter: claimConflictRequeueAfter}, nil
	}
	if err != nil {
		logger.Error(err, "failed to patch byohost")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully attached Byohost", "byohost", host.Name)
	machineScope.ByoHost = &host
	return ctrl.Result{}, nil
}

// ByoHostToByoMachine is a handler.MapFunc that returns a reconciliation
// request for the ByoMachine that a ByoHost is attached to.
func ByoHostToByoMachine(_ context.Context, o client.Object) []reconcile.Request {
	h, ok := o.(*infrav1.ByoHost)
	if !ok || h.Spec.ByoMachineRef == nil {
		return nil
	}
	return []reconcile.Request{
		{
			NamespacedName: client.ObjectKey{
				Namespace: h.Namespace,
				Name:      h.Spec.ByoMachineRef.Name,
			},
		},
	}
}

func (r *ByoMachineReconciler) getInstallerConfig(ctx context.Context, byoMachine *infrav1.ByoMachine) (*unstructured.Unstructured, error) {
	installerConfig := &unstructured.Unstructured{}
	gvk := byoMachine.Spec.InstallerRef.GroupVersionKind()
	gvk.Kind = strings.ReplaceAll(gvk.Kind, "Template", "")
	installerConfig.SetGroupVersionKind(gvk)
	installerConfigName := client.ObjectKey{
		Namespace: byoMachine.Namespace,
		Name:      byoMachine.Name,
	}
	if err := r.Client.Get(ctx, installerConfigName, installerConfig); err != nil {
		return nil, err
	}
	return installerConfig, nil
}

func (r *ByoMachineReconciler) createInstallerConfig(ctx context.Context, machineScope *byoMachineScope) error {
	logger := log.FromContext(ctx).WithValues("cluster", machineScope.Cluster.Name)
	var (
		installerConfig *unstructured.Unstructured
		err             error
	)
	_, err = r.getInstallerConfig(ctx, machineScope.ByoMachine)
	if err != nil && apierrors.IsNotFound(err) {
		template := &unstructured.Unstructured{}
		template.SetGroupVersionKind(machineScope.ByoMachine.Spec.InstallerRef.GroupVersionKind())
		installerTemplateName := client.ObjectKey{
			Namespace: machineScope.ByoMachine.Spec.InstallerRef.Namespace,
			Name:      machineScope.ByoMachine.Spec.InstallerRef.Name,
		}
		if err = r.Client.Get(ctx, installerTemplateName, template); err != nil {
			logger.Error(err, "failed to get installer config template")
			return err
		}
		installerAnnotations := map[string]string{
			infrav1.K8sVersionAnnotation: strings.Split(machineScope.Machine.Spec.Version, "+")[0],
		}
		installerConfig, err = external.GenerateTemplate(&external.GenerateTemplateInput{
			Template:    template,
			TemplateRef: machineScope.ByoMachine.Spec.InstallerRef,
			Namespace:   machineScope.ByoMachine.Namespace,
			Annotations: installerAnnotations,
			ClusterName: machineScope.Cluster.Name,
			OwnerRef:    metav1.NewControllerRef(machineScope.ByoMachine, machineScope.ByoMachine.GroupVersionKind()),
		})
		if err != nil {
			return err
		} else {
			installerConfig.SetName(machineScope.ByoMachine.Name)
			if err = r.Client.Create(ctx, installerConfig); err != nil {
				logger.Error(err, "failed to create installer config")
				return err
			}
		}
	} else if err != nil {
		logger.Error(err, "failed to get installer config")
		return err
	}
	return nil
}
