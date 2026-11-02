// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// K8sInstallerConfigFinalizer allows ReconcileK8sInstallerConfig to clean up secret
	// resources associated with K8sInstallerConfig before removing it from the
	// API Server.
	K8sInstallerConfigFinalizer = "k8sinstallerconfig.infrastructure.cluster.x-k8s.io"
)

// K8sInstallerConfigSpec defines the desired state of K8sInstallerConfig
type K8sInstallerConfigSpec struct {
	// KubernetesRepository is the base URL of the apt repositories of the
	// Kubernetes packages. The installer adds "/v<major>.<minor>/deb/" to it,
	// so a mirror must have the same layout as pkgs.k8s.io. Defaults to
	// https://pkgs.k8s.io/core:/stable:.
	// +optional
	// +kubebuilder:validation:Pattern=`^https?://[^\s'"]+$`
	KubernetesRepository string `json:"kubernetesRepository,omitempty"`

	// ContainerdVersion is the version of containerd to install, such as
	// "2.3.7". It must be 2.0.0 or later. Defaults to the version that the
	// installer is tested with.
	// +optional
	// +kubebuilder:validation:Pattern=`^\d+\.\d+\.\d+$`
	ContainerdVersion string `json:"containerdVersion,omitempty"`

	// ContainerdReleaseURL is the base URL of the containerd releases. A
	// mirror must have the same layout as the releases on GitHub. Defaults to
	// https://github.com/containerd/containerd/releases/download.
	// +optional
	// +kubebuilder:validation:Pattern=`^https?://[^\s'"]+$`
	ContainerdReleaseURL string `json:"containerdReleaseURL,omitempty"`

	// RuncVersion is the version of runc to install, such as "1.5.2".
	// Defaults to the version that the installer is tested with.
	// +optional
	// +kubebuilder:validation:Pattern=`^\d+\.\d+\.\d+$`
	RuncVersion string `json:"runcVersion,omitempty"`

	// RuncReleaseURL is the base URL of the runc releases. A mirror must have
	// the same layout as the releases on GitHub. Defaults to
	// https://github.com/opencontainers/runc/releases/download.
	// +optional
	// +kubebuilder:validation:Pattern=`^https?://[^\s'"]+$`
	RuncReleaseURL string `json:"runcReleaseURL,omitempty"`
}

// K8sInstallerConfigStatus defines the observed state of K8sInstallerConfig
type K8sInstallerConfigStatus struct {
	// Ready indicates the InstallationSecret field is ready to be consumed
	// +optional
	Ready bool `json:"ready,omitempty"`

	// InstallationSecret is an optional reference to a generated installation secret by K8sInstallerConfig controller
	// +optional
	InstallationSecret *corev1.ObjectReference `json:"installationSecret,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// K8sInstallerConfig is the Schema for the k8sinstallerconfigs API
type K8sInstallerConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   K8sInstallerConfigSpec   `json:"spec,omitempty"`
	Status K8sInstallerConfigStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// K8sInstallerConfigList contains a list of K8sInstallerConfig
type K8sInstallerConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []K8sInstallerConfig `json:"items"`
}

func init() {
	objectTypes = append(objectTypes, &K8sInstallerConfig{}, &K8sInstallerConfigList{})
}
