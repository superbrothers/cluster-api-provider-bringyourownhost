// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

// installer-scripts writes the install and uninstall scripts that the default
// installer generates for a host, with the default options, so that
// hack/test-installer.sh can run them on a host without a management cluster.
//
// Usage: go run ./hack/installer-scripts --os-image "Ubuntu 26.04.1 LTS" --arch amd64 --kubernetes-version v1.36.6 --out-dir DIR
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/superbrothers/cluster-api-provider-bringyourownhost/installer"
)

func main() {
	osImage := flag.String("os-image", "", "the PRETTY_NAME of /etc/os-release of the host, such as \"Ubuntu 26.04.1 LTS\"")
	arch := flag.String("arch", "", "the architecture of the host, such as amd64")
	version := flag.String("kubernetes-version", "", "the version of Kubernetes to install, such as v1.36.6")
	outDir := flag.String("out-dir", ".", "the directory to write install.sh and uninstall.sh to")
	flag.Parse()

	if err := run(*osImage, *arch, *version, *outDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// scriptMode is the mode of the scripts, which the test passes to bash -c as
// the host agent does, instead of executing them.
const scriptMode = 0o644

func run(osImage, arch, version, outDir string) error {
	i, err := installer.NewInstaller(osImage, arch, version, nil)
	if err != nil {
		return fmt.Errorf("creating the installer for %q on %s with Kubernetes %s: %w", osImage, arch, version, err)
	}
	for name, script := range map[string]string{"install.sh": i.Install(), "uninstall.sh": i.Uninstall()} {
		if err := os.WriteFile(filepath.Join(outDir, name), []byte(script), scriptMode); err != nil { //nolint:gosec // The scripts are not secrets.
			return err
		}
	}
	return nil
}
