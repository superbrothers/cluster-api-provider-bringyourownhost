// Copyright 2022 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package installer_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/superbrothers/cluster-api-provider-bringyourownhost/installer"
)

var _ = Describe("Byohost Installer Tests", func() {
	DescribeTable("creates the installer for the supported OS and architecture",
		func(osImage, arch string) {
			i, err := installer.NewInstaller(osImage, arch, "v1.36.6", nil)
			Expect(err).ShouldNot(HaveOccurred())
			Expect(i.Install()).To(ContainSubstring("ARCH='" + arch + "'"))
		},
		Entry("Ubuntu 24.04 on amd64", "Ubuntu 24.04.4 LTS", "amd64"),
		Entry("Ubuntu 24.04 on arm64", "Ubuntu 24.04 LTS", "arm64"),
		Entry("Ubuntu 26.04 on amd64", "Ubuntu 26.04.1 LTS", "amd64"),
		Entry("Ubuntu 26.04 on arm64", "Ubuntu 26.04 LTS", "arm64"),
	)

	DescribeTable("fails for the unsupported OS or architecture",
		func(osImage, arch string) {
			_, err := installer.NewInstaller(osImage, arch, "v1.36.6", nil)
			Expect(err).To(MatchError(installer.ErrOsK8sNotSupported))
		},
		Entry("Ubuntu 22.04", "Ubuntu 22.04.5 LTS", "amd64"),
		Entry("Ubuntu 26.10", "Ubuntu 26.10", "amd64"),
		Entry("another distribution", "Debian GNU/Linux 13 (trixie)", "amd64"),
		Entry("an unsupported architecture", "Ubuntu 26.04.1 LTS", "riscv64"),
	)

	DescribeTable("fails for the k8s version that is not in the form of vX.Y.Z",
		func(version string) {
			_, err := installer.NewInstaller("Ubuntu 26.04.1 LTS", "amd64", version, nil)
			Expect(err).To(MatchError(installer.ErrK8sVersionNotSupported))
		},
		Entry("an empty version", ""),
		Entry("a version without the patch", "v1.36"),
		Entry("a version without v", "1.36.6"),
		Entry("a pre-release version", "v1.37.0-rc.1"),
	)

	DescribeTable("fails for the invalid versions of containerd and runc",
		func(opts installer.Options) {
			_, err := installer.NewInstaller("Ubuntu 26.04.1 LTS", "amd64", "v1.36.6", &opts)
			Expect(err).To(MatchError(installer.ErrInvalidVersion))
		},
		Entry("containerd with v", installer.Options{ContainerdVersion: "v2.3.7"}),
		Entry("containerd without the patch", installer.Options{ContainerdVersion: "2.3"}),
		Entry("runc with a quote", installer.Options{RuncVersion: "1.5.2'"}),
	)

	It("fails for containerd older than 2.0.0", func() {
		_, err := installer.NewInstaller("Ubuntu 26.04.1 LTS", "amd64", "v1.36.6", &installer.Options{ContainerdVersion: "1.7.36"})
		Expect(err).To(MatchError(installer.ErrContainerdVersionNotSupported))
	})

	DescribeTable("fails for the invalid URLs",
		func(opts installer.Options) {
			_, err := installer.NewInstaller("Ubuntu 26.04.1 LTS", "amd64", "v1.36.6", &opts)
			Expect(err).To(MatchError(installer.ErrInvalidURL))
		},
		Entry("a Kubernetes repository without a scheme", installer.Options{KubernetesRepository: "pkgs.k8s.io/core:/stable:"}),
		Entry("a Kubernetes repository with a single quote", installer.Options{KubernetesRepository: "https://example.com/'; rm -rf /; '"}),
		Entry("a containerd release URL with a space", installer.Options{ContainerdReleaseURL: "https://example.com/a b"}),
		Entry("a runc release URL with a double quote", installer.Options{RuncReleaseURL: `https://example.com/"`}),
	)

	It("installs the defaults", func() {
		i, err := installer.NewInstaller("Ubuntu 26.04.1 LTS", "amd64", "v1.36.6", nil)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(i.Install()).To(ContainSubstring("KUBERNETES_VERSION='1.36.6'"))
		Expect(i.Install()).To(ContainSubstring("KUBERNETES_REPOSITORY='https://pkgs.k8s.io/core:/stable:/v1.36/deb/'"))
		Expect(i.Install()).To(ContainSubstring(`"kubelet=${KUBERNETES_VERSION}-*"`))
		Expect(i.Install()).To(ContainSubstring("CONTAINERD_VERSION='" + installer.DefaultContainerdVersion + "'"))
		Expect(i.Install()).To(ContainSubstring("CONTAINERD_RELEASE_URL='https://github.com/containerd/containerd/releases/download'"))
		Expect(i.Install()).To(ContainSubstring("RUNC_VERSION='" + installer.DefaultRuncVersion + "'"))
		Expect(i.Install()).To(ContainSubstring("RUNC_RELEASE_URL='https://github.com/opencontainers/runc/releases/download'"))
	})

	It("installs the given versions from the given URLs", func() {
		i, err := installer.NewInstaller("Ubuntu 26.04.1 LTS", "amd64", "v1.37.2", &installer.Options{
			KubernetesRepository: "https://mirror.example.com/kubernetes/",
			ContainerdVersion:    "2.4.2",
			ContainerdReleaseURL: "https://mirror.example.com/containerd/",
			RuncVersion:          "1.4.3",
			RuncReleaseURL:       "https://mirror.example.com/runc",
		})
		Expect(err).ShouldNot(HaveOccurred())
		Expect(i.Install()).To(ContainSubstring("KUBERNETES_REPOSITORY='https://mirror.example.com/kubernetes/v1.37/deb/'"))
		Expect(i.Install()).To(ContainSubstring("CONTAINERD_VERSION='2.4.2'"))
		Expect(i.Install()).To(ContainSubstring("CONTAINERD_RELEASE_URL='https://mirror.example.com/containerd'"))
		Expect(i.Install()).To(ContainSubstring("RUNC_VERSION='1.4.3'"))
		Expect(i.Install()).To(ContainSubstring("RUNC_RELEASE_URL='https://mirror.example.com/runc'"))
	})

	It("installs the systemd unit of containerd", func() {
		i, err := installer.NewInstaller("Ubuntu 26.04.1 LTS", "amd64", "v1.36.6", nil)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(i.Install()).To(ContainSubstring("ExecStart=/usr/local/bin/containerd\n"))
	})

	It("does not leave template actions in the scripts, which the host agent parses again", func() {
		i, err := installer.NewInstaller("Ubuntu 26.04.1 LTS", "amd64", "v1.36.6", nil)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(i.Install()).NotTo(ContainSubstring("{{"))
		Expect(i.Uninstall()).NotTo(ContainSubstring("{{"))
	})
})
