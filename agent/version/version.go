// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// noTagVersion is the version of a build without a release tag, which
// hack/version.sh also gives as v0.0.0-<commit>.
const noTagVersion = "v0.0.0"

// commitLength is the length of the commit in a version without a release tag,
// the same as "git describe --abbrev=14" in hack/version.sh.
const commitLength = 14

// The keys of the VCS information that go build records in the binary.
const (
	vcsRevisionKey = "vcs.revision"
	vcsModifiedKey = "vcs.modified"
)

var (
	GitMajor     string // major version, always numeric
	GitMinor     string // minor version, numeric possibly followed by "+"
	GitVersion   string // semantic version, derived by build scripts
	GitCommit    string // sha1 from git, output of $(git rev-parse HEAD)
	GitTreeState string // state of git tree, either "clean" or "dirty"
	BuildDate    string // build date in ISO8601 format, output of $(date -u +'%Y-%m-%dT%H:%M:%SZ')
)

// Info exposes information about the version used for the current running code.
type Info struct {
	Major        string `json:"major,omitempty"`
	Minor        string `json:"minor,omitempty"`
	GitVersion   string `json:"gitVersion,omitempty"`
	GitCommit    string `json:"gitCommit,omitempty"`
	GitTreeState string `json:"gitTreeState,omitempty"`
	BuildDate    string `json:"buildDate,omitempty"`
	GoVersion    string `json:"goVersion,omitempty"`
	Compiler     string `json:"compiler,omitempty"`
	Platform     string `json:"platform,omitempty"`
}

// AgentVersion returns the version that the agent reports to its ByoHost. It
// is GitVersion, which the build embeds with hack/version.sh. A plain go build,
// such as the agent that the e2e builds, has no GitVersion, so the version is
// made from the commit that Go records in the binary, or v0.0.0-unknown.
func AgentVersion() string {
	// info is nil when the binary has no build information.
	info, _ := debug.ReadBuildInfo()
	return agentVersion(GitVersion, info)
}

func agentVersion(gitVersion string, info *debug.BuildInfo) string {
	if gitVersion != "" {
		return gitVersion
	}
	var revision string
	var modified bool
	if info != nil {
		for _, s := range info.Settings {
			switch s.Key {
			case vcsRevisionKey:
				revision = s.Value
			case vcsModifiedKey:
				modified = s.Value == "true"
			}
		}
	}
	if revision == "" {
		return noTagVersion + "-unknown"
	}
	if len(revision) > commitLength {
		revision = revision[:commitLength]
	}
	v := noTagVersion + "-" + revision
	if modified {
		v += "-dirty"
	}
	return v
}

// Get returns an Info object with all the information about the current running code.
func Get() Info {
	return Info{
		Major:        GitMajor,
		Minor:        GitMinor,
		GitVersion:   GitVersion,
		GitCommit:    GitCommit,
		GitTreeState: GitTreeState,
		BuildDate:    BuildDate,
		GoVersion:    runtime.Version(),
		Compiler:     runtime.Compiler,
		Platform:     fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
	}
}
