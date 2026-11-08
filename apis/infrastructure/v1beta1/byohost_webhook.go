// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	v1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

//+kubebuilder:webhook:path=/validate-infrastructure-cluster-x-k8s-io-v1beta1-byohost,mutating=false,failurePolicy=fail,sideEffects=None,groups=infrastructure.cluster.x-k8s.io,resources=byohosts,verbs=create;update;delete,versions=v1beta1,name=vbyohost.kb.io,admissionReviewVersions={v1,v1beta1}

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
)

// Handle handles all the requests for ByoHost resource
//
//nolint:gocritic
func (v *ByoHostValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	var response admission.Response

	switch req.Operation {
	case v1.Create, v1.Update:
		response = v.handleCreateUpdate(&req)
	case v1.Delete:
		response = v.handleDelete(&req)
	default:
		response = admission.Allowed("")
	}
	return response
}

func (v *ByoHostValidator) handleCreateUpdate(req *admission.Request) admission.Response {
	byoHost := &ByoHost{}
	err := v.decoder.Decode(*req, byoHost)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	// Leave the requests of the other users, such as the manager, the
	// administrators and clusterctl, to RBAC.
	if !slices.Contains(req.UserInfo.Groups, HostsGroup) {
		return admission.Allowed("")
	}
	// An agent can create and update only the ByoHost of its own host.
	userName := req.UserInfo.Username
	if userName != HostUsernamePrefix+byoHost.Name {
		return admission.Denied(fmt.Sprintf("%s cannot create/update ByoHost %s: a member of %s can create/update only the ByoHost named after its username %s<name>", userName, byoHost.Name, HostsGroup, HostUsernamePrefix))
	}
	return admission.Allowed("")
}

func (v *ByoHostValidator) handleDelete(req *admission.Request) admission.Response {
	byoHost := &ByoHost{}
	err := v.decoder.DecodeRaw(req.OldObject, byoHost)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	// A ByoHost in use is never deleted: its host has not been reset, and the
	// agent would register the host again as a free ByoHost when it restarts.
	if byoHost.Spec.ByoMachineRef != nil {
		return admission.Denied(fmt.Sprintf("cannot delete ByoHost %s while it is attached to ByoMachine %s (spec.byoMachineRef)", byoHost.Name, byoHost.Spec.ByoMachineRef.Name))
	}
	return admission.Allowed("")
}
