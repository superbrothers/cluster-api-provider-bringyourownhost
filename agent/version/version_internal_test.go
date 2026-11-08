// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package version

import (
	"runtime/debug"
	"testing"
)

func TestAgentVersion(t *testing.T) {
	const revision = "3b9fb48a7a58ceb3ab8e410b9633a1194b29ca08"
	vcs := func(modified string) *debug.BuildInfo {
		return &debug.BuildInfo{Settings: []debug.BuildSetting{
			{Key: "vcs", Value: "git"},
			{Key: vcsRevisionKey, Value: revision},
			{Key: vcsModifiedKey, Value: modified},
		}}
	}

	tests := []struct {
		name       string
		gitVersion string
		info       *debug.BuildInfo
		want       string
	}{
		{
			name:       "the version that the build embeds",
			gitVersion: "v0.6.0",
			info:       vcs("false"),
			want:       "v0.6.0",
		},
		{
			name: "the commit of a plain go build",
			info: vcs("false"),
			want: "v0.0.0-3b9fb48a7a58ce",
		},
		{
			name: "the commit of a plain go build of a dirty tree",
			info: vcs("true"),
			want: "v0.0.0-3b9fb48a7a58ce-dirty",
		},
		{
			name: "a build without the commit, such as a test binary",
			info: &debug.BuildInfo{},
			want: "v0.0.0-unknown",
		},
		{
			name: "a binary without build information",
			want: "v0.0.0-unknown",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := agentVersion(tt.gitVersion, tt.info); got != tt.want {
				t.Errorf("agentVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}
