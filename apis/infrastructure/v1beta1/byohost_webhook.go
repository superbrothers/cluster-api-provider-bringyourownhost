// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	v1 "k8s.io/api/admission/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

//+kubebuilder:webhook:path=/validate-infrastructure-cluster-x-k8s-io-v1beta1-byohost,mutating=false,failurePolicy=fail,sideEffects=None,groups=infrastructure.cluster.x-k8s.io,resources=byohosts;byohosts/status,verbs=create;update;delete,versions=v1beta1,name=vbyohost.kb.io,admissionReviewVersions={v1,v1beta1}

// +k8s:deepcopy-gen=false
// ByoHostValidator validates ByoHosts
type ByoHostValidator struct {
	decoder admission.Decoder
}

// NewByoHostValidator returns a ByoHostValidator that decodes the objects in
// admission requests with the given scheme.
func NewByoHostValidator(scheme *runtime.Scheme) *ByoHostValidator {
	return &ByoHostValidator{decoder: admission.NewDecoder(scheme)}
}

const (
	// HostsGroup is the group of the agents. An agent authenticates with the
	// client certificate that it gets for this group from a CSR
	// (agent/registration/csr.go).
	HostsGroup = "byoh:hosts"
	// HostUsernamePrefix is the prefix of the username of an agent, which is
	// followed by the name of its host, and so of its ByoHost.
	HostUsernamePrefix = "byoh:host:"
	// statusSubResource is the subresource of the status of a ByoHost.
	statusSubResource = "status"
	// updateStatusVerb is the verb of an update of the status in the
	// messages.
	updateStatusVerb = "update the status of"
)

// Handle handles all the requests for ByoHost resource.
//
// An agent creates the ByoHost of its host, and then writes only its status:
// the controller writes the spec, and the administrators write the labels and
// the annotations (docs/host_lifecycle.md). RBAC lets the agents create
// ByoHosts and update their status; this webhook limits each agent to its own
// ByoHost, and rejects what RBAC rejects too, as a second line of defense. The
// requests of the other users, such as the manager, the administrators and
// clusterctl, are left to RBAC, except the deletion of a ByoHost in use.
//
//nolint:gocritic
func (v *ByoHostValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	var response admission.Response

	switch req.Operation {
	case v1.Create:
		response = v.handleCreate(&req)
	case v1.Update:
		response = v.handleUpdate(&req)
	case v1.Delete:
		response = v.handleDelete(&req)
	default:
		response = admission.Allowed("")
	}
	return response
}

// isHost returns whether the request is from an agent.
func isHost(req *admission.Request) bool {
	return slices.Contains(req.UserInfo.Groups, HostsGroup)
}

// checkHostName returns a response that denies the request when the agent is
// not the agent of the host of the ByoHost, or nil.
func checkHostName(req *admission.Request, byoHost *ByoHost) *admission.Response {
	userName := req.UserInfo.Username
	if userName == HostUsernamePrefix+byoHost.Name {
		return nil
	}
	response := admission.Denied(fmt.Sprintf("%s cannot %s ByoHost %s: a member of %s can create, and update the status of, only the ByoHost named after its username %s<name>", userName, verb(req), byoHost.Name, HostsGroup, HostUsernamePrefix))
	return &response
}

// verb returns the verb of the request in the messages, such as "update the
// status of".
func verb(req *admission.Request) string {
	if req.Operation == v1.Update && req.SubResource == statusSubResource {
		return updateStatusVerb
	}
	return strings.ToLower(string(req.Operation))
}

func (v *ByoHostValidator) handleCreate(req *admission.Request) admission.Response {
	byoHost := &ByoHost{}
	if err := v.decoder.Decode(*req, byoHost); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if !isHost(req) {
		return admission.Allowed("")
	}
	if response := checkHostName(req, byoHost); response != nil {
		return *response
	}
	if errs := validateHostCreate(byoHost); len(errs) > 0 {
		return admission.Denied(fmt.Sprintf("%s cannot create ByoHost %s: %s", req.UserInfo.Username, byoHost.Name, errs.ToAggregate().Error()))
	}
	return admission.Allowed("")
}

func (v *ByoHostValidator) handleUpdate(req *admission.Request) admission.Response {
	byoHost := &ByoHost{}
	if err := v.decoder.Decode(*req, byoHost); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if !isHost(req) {
		return admission.Allowed("")
	}
	if req.SubResource != statusSubResource {
		return admission.Denied(fmt.Sprintf("%s cannot update ByoHost %s: a member of %s can update only the status of a ByoHost", req.UserInfo.Username, byoHost.Name, HostsGroup))
	}
	if response := checkHostName(req, byoHost); response != nil {
		return *response
	}
	return admission.Allowed("")
}

func (v *ByoHostValidator) handleDelete(req *admission.Request) admission.Response {
	byoHost := &ByoHost{}
	err := v.decoder.DecodeRaw(req.OldObject, byoHost)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	// The agents never delete ByoHosts.
	if isHost(req) {
		return admission.Denied(fmt.Sprintf("%s cannot delete ByoHost %s: a member of %s cannot delete ByoHosts", req.UserInfo.Username, byoHost.Name, HostsGroup))
	}
	// A ByoHost in use is never deleted: its host has not been reset, and the
	// agent would register the host again as a free ByoHost when it restarts.
	if byoHost.Spec.ByoMachineRef != nil {
		return admission.Denied(fmt.Sprintf("cannot delete ByoHost %s while it is attached to ByoMachine %s (spec.byoMachineRef)", byoHost.Name, byoHost.Spec.ByoMachineRef.Name))
	}
	return admission.Allowed("")
}

// validateHostCreate validates a ByoHost that an agent creates. The agent
// creates it with the labels of its --label flags, and with nothing else
// (agent/registration/host_registrar.go). The API server drops the status of a
// ByoHost in a creation.
func validateHostCreate(byoHost *ByoHost) field.ErrorList {
	var errs field.ErrorList
	metadata := field.NewPath("metadata")
	for _, key := range sortedKeys(byoHost.Labels) {
		if isReservedKey(key) {
			errs = append(errs, field.Forbidden(metadata.Child("labels").Key(key), "a host cannot set the labels of Cluster API and BYOH"))
		}
	}
	if len(byoHost.Annotations) > 0 {
		errs = append(errs, field.Forbidden(metadata.Child("annotations"), "a host cannot set annotations"))
	}
	if len(byoHost.OwnerReferences) > 0 {
		errs = append(errs, field.Forbidden(metadata.Child("ownerReferences"), "a host cannot set owner references"))
	}
	if len(byoHost.Finalizers) > 0 {
		errs = append(errs, field.Forbidden(metadata.Child("finalizers"), "a host cannot set finalizers"))
	}
	if !apiequality.Semantic.DeepEqual(byoHost.Spec, ByoHostSpec{}) {
		errs = append(errs, field.Forbidden(field.NewPath("spec"), "only the controller sets the spec"))
	}
	return errs
}

// isReservedKey returns whether a label key belongs to Cluster API or BYOH:
// its prefix is cluster.x-k8s.io or a subdomain of it, such as
// byoh.infrastructure.cluster.x-k8s.io.
func isReservedKey(key string) bool {
	prefix, _, found := strings.Cut(key, "/")
	if !found {
		return false
	}
	return prefix == "cluster.x-k8s.io" || strings.HasSuffix(prefix, ".cluster.x-k8s.io")
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
