// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta1"
)

const (
	// K8sVersionAnnotation is the annotation of a K8sInstallerConfig with the
	// version of Kubernetes to install. The ByoMachine controller sets it from
	// the version of the Machine when it creates the K8sInstallerConfig.
	K8sVersionAnnotation = "byoh.infrastructure.cluster.x-k8s.io/k8sversion"
)

// ByoHostSpec defines the desired state of ByoHost. The ByoMachine controller
// writes it, and the host agent only reads it.
type ByoHostSpec struct {
	// ByoMachineRef is the ByoMachine that the host is attached to, in the
	// namespace of the ByoHost. The host is in use while it is set: the
	// controller attaches only a ByoHost without it to a ByoMachine.
	// +optional
	ByoMachineRef *ByoMachineReference `json:"byoMachineRef,omitempty"`

	// ResetRequested asks the host agent to reset the host, so that the host
	// can be attached to a ByoMachine again. The controller sets it when the
	// ByoMachine in ByoMachineRef is deleted. Resetting the host runs kubeadm
	// reset, uninstalls the Kubernetes components that the agent has
	// installed, and cleans up the files of the bootstrap. The agent reports
	// that it has reset the host in status.lastReset, and the controller then
	// detaches the host from the ByoMachine.
	// +optional
	ResetRequested bool `json:"resetRequested,omitempty"`

	// BootstrapSecret is the Secret with the bootstrap data of the Machine of
	// the ByoMachine in ByoMachineRef.
	// +optional
	BootstrapSecret *corev1.ObjectReference `json:"bootstrapSecret,omitempty"`

	// InstallationSecret is the Secret with the scripts that install and
	// uninstall the Kubernetes components, which the installer controller
	// generates for the ByoMachine in ByoMachineRef.
	// +optional
	InstallationSecret *corev1.ObjectReference `json:"installationSecret,omitempty"`
}

// ByoMachineReference refers to a ByoMachine in the namespace of the ByoHost.
type ByoMachineReference struct {
	// Name is the name of the ByoMachine.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// UID is the UID of the ByoMachine. It tells the attachments of the host
	// apart, so that a report of a reset for an earlier ByoMachine does not
	// detach the host from a later one.
	// +required
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:MinLength=1
	UID types.UID `json:"uid"`
}

// LastReset is the last reset of the host by the host agent.
type LastReset struct {
	// ByoMachineUID is the UID in spec.byoMachineRef when the host agent reset
	// the host.
	// +required
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:MinLength=1
	ByoMachineUID types.UID `json:"byoMachineUID"`

	// Time is when the host agent finished resetting the host.
	// +required
	Time metav1.Time `json:"time"`
}

// HostInfo is the platform of a host, which the host agent reports. The
// fields are named after those of the NodeSystemInfo of a Node.
type HostInfo struct {
	// OperatingSystem is the operating system of the host, such as linux.
	OperatingSystem string `json:"operatingSystem,omitempty"`

	// OSImage is the distribution of the host, from PRETTY_NAME of
	// /etc/os-release, such as Ubuntu 26.04 LTS.
	OSImage string `json:"osImage,omitempty"`

	// Architecture is the architecture of the host, such as amd64.
	Architecture string `json:"architecture,omitempty"`
}

// AgentInfo is what the host agent reports about itself every time it starts.
type AgentInfo struct {
	// Version is the version of the host agent. The version is embedded in the
	// build, so a reported AgentInfo always has it; a build without a release
	// tag reports v0.0.0 with the commit, such as v0.0.0-3b9fb48a7a58ce.
	// +required
	// +kubebuilder:validation:MinLength=1
	Version string `json:"version"`
}

// ByoHostStatus defines the observed state of ByoHost. The host agent writes
// it.
type ByoHostStatus struct {
	// Conditions defines current service state of the BYOMachine.
	// +optional
	Conditions clusterv1.Conditions `json:"conditions,omitempty"`

	// HostInfo is the platform of the host.
	// +optional
	HostInfo HostInfo `json:"hostInfo,omitempty"`

	// AgentInfo is what the host agent reports about itself. It is absent
	// until the agent reports it.
	// +optional
	AgentInfo *AgentInfo `json:"agentInfo,omitempty"`

	// Network returns the network status for each of the host's configured
	// network interfaces.
	// +optional
	Network []NetworkStatus `json:"network,omitempty"`

	// LastReset is the last reset of the host that the host agent has
	// finished, for spec.resetRequested. It is absent until the agent first
	// resets the host.
	// +optional
	LastReset *LastReset `json:"lastReset,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:path=byohosts,scope=Namespaced,shortName=byoh
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="ByoMachine",type="string",JSONPath=`.spec.byoMachineRef.name`
//+kubebuilder:printcolumn:name="OSImage",type="string",JSONPath=`.status.hostInfo.osImage`
//+kubebuilder:printcolumn:name="Arch",type="string",JSONPath=`.status.hostInfo.architecture`
//+kubebuilder:printcolumn:name="AgentVersion",type="string",JSONPath=`.status.agentInfo.version`
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// ByoHost is the Schema for the byohosts API
type ByoHost struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ByoHostSpec   `json:"spec,omitempty"`
	Status ByoHostStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ByoHostList contains a list of ByoHost
type ByoHostList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ByoHost `json:"items"`
}

func init() {
	objectTypes = append(objectTypes, &ByoHost{}, &ByoHostList{})
}

// GetConditions gets the ByoHost status conditions
func (byoHost *ByoHost) GetConditions() clusterv1.Conditions {
	return byoHost.Status.Conditions
}

// SetConditions sets the ByoHost status conditions
func (byoHost *ByoHost) SetConditions(conditions clusterv1.Conditions) {
	byoHost.Status.Conditions = conditions
}
