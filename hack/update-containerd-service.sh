#!/usr/bin/env bash
# Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
# SPDX-License-Identifier: Apache-2.0

# Updates the systemd unit of containerd that the default installer embeds to
# the one of DefaultContainerdVersion in installer/installer.go. The releases
# of containerd do not include the unit, so it comes from the repository.

set -o errexit
set -o nounset
set -o pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
version=$(sed -n 's/^\s*DefaultContainerdVersion\s*=\s*"\([^"]*\)"$/\1/p' "${root}/installer/installer.go")
if [[ -z "${version}" ]]; then
    echo "DefaultContainerdVersion is not found in installer/installer.go" >&2
    exit 1
fi

service="${root}/installer/internal/algo/containerd.service"
curl -fsSL -o "${service}.tmp" "https://raw.githubusercontent.com/containerd/containerd/v${version}/containerd.service"
mv "${service}.tmp" "${service}"
echo "Updated ${service#"${root}/"} to the one of containerd v${version}"
