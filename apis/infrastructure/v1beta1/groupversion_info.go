// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// Package v1beta1 contains API Schema definitions for the infrastructure v1beta1 API group
// +kubebuilder:object:generate=true
// +groupName=infrastructure.cluster.x-k8s.io
package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is group version used to register these objects
	GroupVersion = schema.GroupVersion{Group: "infrastructure.cluster.x-k8s.io", Version: "v1beta1"}

	// schemeBuilder is used to add go types to the GroupVersionKind scheme
	schemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = schemeBuilder.AddToScheme

	// objectTypes holds the types in this group-version, which the init
	// functions of the types register.
	objectTypes = []runtime.Object{}
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion, objectTypes...)
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}
