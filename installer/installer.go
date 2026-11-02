// Copyright 2022 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package installer

import (
	"regexp"
	"strings"

	"github.com/superbrothers/cluster-api-provider-bringyourownhost/installer/internal/algo"
)

// K8sInstaller represent k8s installer interface
type K8sInstaller interface {
	Install() string
	Uninstall() string
}

// Error string wrapper for errors returned by the installer
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrOsK8sNotSupported error type when the OS is not supported by the k8s installer
	ErrOsK8sNotSupported = Error("No k8s support for OS")
	// ErrK8sVersionNotSupported error type when the k8s version is not in the form of vX.Y.Z
	ErrK8sVersionNotSupported = Error("k8s version must be in the form of vX.Y.Z")
	// ErrInvalidVersion error type when the version of containerd or runc is not in the form of X.Y.Z
	ErrInvalidVersion = Error("containerd and runc versions must be in the form of X.Y.Z")
	// ErrContainerdVersionNotSupported error type when the version of containerd is older than 2.0.0
	ErrContainerdVersionNotSupported = Error("containerd version must be 2.0.0 or later")
	// ErrInvalidURL error type when a URL to download from is invalid
	ErrInvalidURL = Error("URLs must be http or https URLs without whitespace or quotes")
)

const (
	// DefaultKubernetesRepository is the base URL of the apt repositories of
	// Kubernetes, which has a repository for each minor version.
	DefaultKubernetesRepository = "https://pkgs.k8s.io/core:/stable:"
	// DefaultContainerdVersion is the version of containerd to install. It is
	// an LTS release that supports the Kubernetes versions in the README. When
	// it changes, run `make update-containerd-service` to update the systemd
	// unit of containerd, and update the tested versions in the README.
	DefaultContainerdVersion = "2.3.7"
	// DefaultContainerdReleaseURL is the base URL of the containerd releases.
	DefaultContainerdReleaseURL = "https://github.com/containerd/containerd/releases/download"
	// DefaultRuncVersion is the version of runc to install, which
	// DefaultContainerdVersion is tested with.
	DefaultRuncVersion = "1.5.2"
	// DefaultRuncReleaseURL is the base URL of the runc releases.
	DefaultRuncReleaseURL = "https://github.com/opencontainers/runc/releases/download"
)

var (
	// k8sVersion matches the version of Kubernetes without the build metadata.
	k8sVersion = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)
	// releaseVersion matches the versions of containerd and runc.
	releaseVersion = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	// containerd1 matches the versions of containerd 0.x and 1.x, which do not
	// read the version 3 configuration that the install script writes.
	containerd1 = regexp.MustCompile(`^0*[01]\.`)
	// downloadURL matches the URLs that the scripts can put in single quotes.
	downloadURL = regexp.MustCompile(`^https?://[^\s'"]+$`)
)

// Options are the settings of the installer. Empty fields take the defaults.
type Options struct {
	// KubernetesRepository is the base URL of the apt repositories for each
	// minor version of Kubernetes.
	KubernetesRepository string
	// ContainerdVersion is the version of containerd, such as "2.3.7".
	ContainerdVersion string
	// ContainerdReleaseURL is the base URL of the containerd releases, which
	// has the same layout as the releases on GitHub.
	ContainerdReleaseURL string
	// RuncVersion is the version of runc, such as "1.5.2".
	RuncVersion string
	// RuncReleaseURL is the base URL of the runc releases, which has the same
	// layout as the releases on GitHub.
	RuncReleaseURL string
}

// setDefaults sets the defaults in the empty fields.
func (o *Options) setDefaults() {
	if o.KubernetesRepository == "" {
		o.KubernetesRepository = DefaultKubernetesRepository
	}
	if o.ContainerdVersion == "" {
		o.ContainerdVersion = DefaultContainerdVersion
	}
	if o.ContainerdReleaseURL == "" {
		o.ContainerdReleaseURL = DefaultContainerdReleaseURL
	}
	if o.RuncVersion == "" {
		o.RuncVersion = DefaultRuncVersion
	}
	if o.RuncReleaseURL == "" {
		o.RuncReleaseURL = DefaultRuncReleaseURL
	}
}

// validate returns an error when the options cannot go into the scripts.
func (o *Options) validate() error {
	if !releaseVersion.MatchString(o.ContainerdVersion) || !releaseVersion.MatchString(o.RuncVersion) {
		return ErrInvalidVersion
	}
	if containerd1.MatchString(o.ContainerdVersion) {
		return ErrContainerdVersionNotSupported
	}
	for _, u := range []string{o.KubernetesRepository, o.ContainerdReleaseURL, o.RuncReleaseURL} {
		if !downloadURL.MatchString(u) {
			return ErrInvalidURL
		}
	}
	return nil
}

// osInstaller associates the OS images with the installer for them.
type osInstaller struct {
	// osImage matches the PRETTY_NAME of /etc/os-release that the host agent
	// reports, such as "Ubuntu 26.04.1 LTS".
	osImage *regexp.Regexp
	// archs are the architectures that the host agent reports with runtime.GOARCH.
	archs map[string]bool
	new   func(*algo.Input) (K8sInstaller, error)
}

func newUbuntuInstaller(input *algo.Input) (K8sInstaller, error) {
	return algo.NewUbuntuInstaller(input)
}

// ubuntuArchs are the architectures of Ubuntu that the installer supports.
var ubuntuArchs = map[string]bool{"amd64": true, "arm64": true}

// osInstallers are the OSes that the installer supports.
var osInstallers = []osInstaller{
	{
		osImage: regexp.MustCompile(`^Ubuntu 24\.04(\.\d+)? LTS$`),
		archs:   ubuntuArchs,
		new:     newUbuntuInstaller,
	},
	{
		osImage: regexp.MustCompile(`^Ubuntu 26\.04(\.\d+)? LTS$`),
		archs:   ubuntuArchs,
		new:     newUbuntuInstaller,
	},
}

// NewInstaller returns the installer for the OS and the architecture of the
// host, which installs the given version of Kubernetes with the options. opts
// can be nil to use the defaults.
func NewInstaller(osImage, arch, version string, opts *Options) (K8sInstaller, error) {
	var found *osInstaller
	for i := range osInstallers {
		if osInstallers[i].osImage.MatchString(osImage) && osInstallers[i].archs[arch] {
			found = &osInstallers[i]
			break
		}
	}
	if found == nil {
		return nil, ErrOsK8sNotSupported
	}

	m := k8sVersion.FindStringSubmatch(version)
	if m == nil {
		return nil, ErrK8sVersionNotSupported
	}

	o := Options{}
	if opts != nil {
		o = *opts
	}
	o.setDefaults()
	if err := o.validate(); err != nil {
		return nil, err
	}

	return found.new(&algo.Input{
		Arch:                 arch,
		KubernetesVersion:    m[1] + "." + m[2] + "." + m[3],
		KubernetesRepository: strings.TrimSuffix(o.KubernetesRepository, "/") + "/v" + m[1] + "." + m[2] + "/deb/",
		ContainerdVersion:    o.ContainerdVersion,
		ContainerdReleaseURL: strings.TrimSuffix(o.ContainerdReleaseURL, "/"),
		RuncVersion:          o.RuncVersion,
		RuncReleaseURL:       strings.TrimSuffix(o.RuncReleaseURL, "/"),
	})
}
