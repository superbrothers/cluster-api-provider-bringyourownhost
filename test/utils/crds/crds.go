// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

// Package crds locates CRD manifests shipped by Go module dependencies so
// that envtest can load them without hard-coding module versions.
package crds

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

const clusterAPIModule = "sigs.k8s.io/cluster-api"

// ClusterAPICoreDir returns the directory of the Cluster API core CRDs for
// the Cluster API version this module depends on.
func ClusterAPICoreDir() (string, error) {
	dir, err := moduleDir(clusterAPIModule)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "core", "config", "crd", "bases"), nil
}

// ClusterAPIKubeadmBootstrapDir returns the directory of the kubeadm
// bootstrap provider CRDs for the Cluster API version this module depends on.
func ClusterAPIKubeadmBootstrapDir() (string, error) {
	dir, err := moduleDir(clusterAPIModule)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "bootstrap", "kubeadm", "config", "crd", "bases"), nil
}

// moduleDir returns the local directory of a module in the build list of the
// main module, downloading it if necessary.
func moduleDir(module string) (string, error) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", module).Output()
	if err != nil {
		return "", fmt.Errorf("failed to locate module %s: %w", module, err)
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		return "", fmt.Errorf("module %s has no local directory", module)
	}
	return dir, nil
}
