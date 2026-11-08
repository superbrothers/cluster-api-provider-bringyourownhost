#!/usr/bin/env bash
# Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
# SPDX-License-Identifier: Apache-2.0

# Tests the scripts of the default installer on a host of Ubuntu in a
# container, on the architecture of this machine. For each version of
# Kubernetes in turn on the same host, the install script installs it,
# `kubeadm init` brings up a control plane, `kubeadm reset` and the uninstall
# script remove it, and the test checks that nothing the install script added
# is left, so that the next version starts from the same state as a reused
# host does.
#
# Usage: hack/test-installer.sh UBUNTU_VERSION [KUBERNETES_VERSION...]
#
# UBUNTU_VERSION is the tag of the ubuntu image, such as 26.04. The versions of
# Kubernetes default to the tested patch releases in the table of the README.
#
# It needs Docker and Go. The host is a privileged container with systemd, as
# the hosts of the e2e tests are: it loads the kernel modules of the installer
# on this machine and changes the kernel parameters of its own network
# namespace. swapoff and swapon do nothing in it, so that the swap of this
# machine is kept.

set -o errexit
set -o nounset
set -o pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

[ $# -ge 1 ] || { echo "Usage: $0 UBUNTU_VERSION [KUBERNETES_VERSION...]" >&2; exit 2; }
UBUNTU_VERSION=$1
shift
if [ $# -gt 0 ]; then
    VERSIONS=("$@")
else
    # The rows of the table are like "| v1.36      | v1.36.6              |".
    mapfile -t VERSIONS < <(sed -nE 's/^\| *v[0-9]+\.[0-9]+ *\| *(v[0-9]+\.[0-9]+\.[0-9]+) *\|$/\1/p' "${REPO_ROOT}/README.md" | sort -V)
    [ ${#VERSIONS[@]} -gt 0 ] || { echo "No tested Kubernetes versions found in README.md" >&2; exit 1; }
fi

IMAGE=byoh-installer-test:ubuntu-${UBUNTU_VERSION}
NAME=byoh-installer-test-${UBUNTU_VERSION}-$$
# kubeadm names the node after the host, which must be a DNS-1123 name.
HOSTNAME=byoh-installer-test
WORK=$(mktemp -d)

cleanup() {
    docker rm --force --volumes "${NAME}" >/dev/null 2>&1 || true
    rm -rf "${WORK}"
}
trap cleanup EXIT

on_host() {
    docker exec "${NAME}" "$@"
}

# write_on_host writes the standard input to the file on the host.
write_on_host() {
    docker exec --interactive "${NAME}" sh -c "cat > '$1'"
}

kubectl_admin() {
    on_host kubectl --kubeconfig /etc/kubernetes/admin.conf "$@"
}

fail() {
    echo "FAIL: $*" >&2
    on_host systemctl --no-pager status containerd kubelet >&2 || true
    on_host journalctl --no-pager -u containerd -u kubelet -n 100 >&2 || true
    exit 1
}

pass() {
    echo "ok: $* (${SECONDS}s)"
}

# run_script runs a script on the host as the host agent does.
run_script() {
    on_host /bin/bash -c "$(cat "$1")"
}

# wait_for runs the command until it succeeds, for up to the given seconds.
wait_for() {
    local timeout=$1 _
    shift
    for _ in $(seq "${timeout}"); do
        if "$@" >/dev/null 2>&1; then
            return 0
        fi
        sleep 1
    done
    return 1
}

system_is_up() {
    case "$(on_host systemctl is-system-running 2>/dev/null)" in
        running|degraded) return 0 ;;
        *) return 1 ;;
    esac
}

# script_var prints the value of a variable that a script sets, such as
# CONTAINERD_VERSION='2.3.7'.
script_var() {
    sed -nE "s/^$1='([^']*)'\$/\1/p" "$2"
}

# is_installed tells if dpkg has the package installed on the host.
is_installed() {
    # shellcheck disable=SC2016 # dpkg-query expands the field.
    [ "$(on_host dpkg-query -W -f='${db:Status-Status}' "$1" 2>/dev/null)" = installed ]
}

echo "--- Build the host of Ubuntu ${UBUNTU_VERSION}"
# A minimal host: the install script installs what else it needs.
docker build --quiet --tag "${IMAGE}" --build-arg "UBUNTU_VERSION=${UBUNTU_VERSION}" - <<'EOF' >/dev/null
ARG UBUNTU_VERSION
FROM ubuntu:${UBUNTU_VERSION}
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends systemd systemd-sysv dbus kmod \
    && rm -rf /var/lib/apt/lists/*
# The container shares the swap of the machine, so that `swapoff -a` of the
# installer would turn it off. These do nothing instead, as /usr/local/sbin
# comes before /usr/sbin in PATH.
RUN for cmd in swapoff swapon; do \
        printf '#!/bin/sh\n# Does nothing in the host of the installer test.\nexit 0\n' > "/usr/local/sbin/${cmd}"; \
        chmod 755 "/usr/local/sbin/${cmd}"; \
    done
# systemd shuts down on SIGRTMIN+3, and ignores SIGTERM as PID 1.
STOPSIGNAL SIGRTMIN+3
ENTRYPOINT ["/sbin/init"]
EOF
# /var is a volume, as containerd cannot use overlayfs on the overlayfs of the
# container. kubeadm reads the kernel config from /boot for SystemVerification.
docker run --detach --name "${NAME}" --hostname "${HOSTNAME}" \
    --privileged --security-opt seccomp=unconfined \
    --tmpfs /run --tmpfs /tmp --volume /var \
    --volume /lib/modules:/lib/modules:ro --volume /boot:/boot:ro \
    "${IMAGE}" >/dev/null
wait_for 120 system_is_up || fail "systemd did not start"
# shellcheck disable=SC2016 # The shell on the host expands it.
OS_IMAGE=$(on_host bash -c '. /etc/os-release && echo "${PRETTY_NAME}"')
ARCH=$(on_host dpkg --print-architecture)
pass "${OS_IMAGE} on ${ARCH}"

echo "--- Generate the scripts for ${VERSIONS[*]}"
(cd "${REPO_ROOT}" && go build -o "${WORK}/installer-scripts" ./hack/installer-scripts)
for version in "${VERSIONS[@]}"; do
    mkdir -p "${WORK}/${version}"
    "${WORK}/installer-scripts" --os-image "${OS_IMAGE}" --arch "${ARCH}" \
        --kubernetes-version "${version}" --out-dir "${WORK}/${version}"
done
pass "generated"

first=true
for version in "${VERSIONS[@]}"; do
    install=${WORK}/${version}/install.sh
    uninstall=${WORK}/${version}/uninstall.sh
    containerd_version=$(script_var CONTAINERD_VERSION "${install}")
    runc_version=$(script_var RUNC_VERSION "${install}")

    echo "--- ${version}: install"
    run_script "${install}" || fail "the install script failed"
    if ${first}; then
        # The agent runs the script again when it fails on the way, and on a
        # reused host.
        run_script "${install}" || fail "the install script failed when it ran again"
        first=false
    fi
    [ "$(on_host kubeadm version -o short)" = "${version}" ] || fail "kubeadm is not ${version}"
    [ "$(on_host kubelet --version)" = "Kubernetes ${version}" ] || fail "kubelet is not ${version}"
    on_host kubectl version --client | grep -qxF "Client Version: ${version}" || fail "kubectl is not ${version}"
    for pkg in kubelet kubeadm kubectl; do
        on_host apt-mark showhold | grep -qx "${pkg}" || fail "${pkg} is not held"
    done
    [ "$(on_host /usr/local/bin/containerd --version | awk '{print $3}')" = "v${containerd_version}" ] ||
        fail "containerd is not ${containerd_version}"
    [ "$(on_host /usr/local/sbin/runc --version | awk 'NR == 1 {print $3}')" = "${runc_version}" ] ||
        fail "runc is not ${runc_version}"
    [ "$(on_host systemctl is-active containerd)" = active ] || fail "containerd is not running"
    on_host crictl info >/dev/null || fail "crictl cannot connect to containerd"
    pass "installed Kubernetes ${version}, containerd ${containerd_version} and runc ${runc_version}"

    echo "--- ${version}: kubeadm init"
    # The kubelet keeps running with the swap of the machine.
    write_on_host /root/kubeadm.yaml <<EOF
apiVersion: kubeadm.k8s.io/v1beta4
kind: InitConfiguration
nodeRegistration:
  ignorePreflightErrors:
  - Swap
---
apiVersion: kubeadm.k8s.io/v1beta4
kind: ClusterConfiguration
kubernetesVersion: ${version}
---
apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
failSwapOn: false
EOF
    on_host kubeadm init --config /root/kubeadm.yaml >"${WORK}/kubeadm-init.log" 2>&1 ||
        { cat "${WORK}/kubeadm-init.log" >&2; fail "kubeadm init failed"; }
    [ "$(kubectl_admin get --raw /readyz)" = ok ] || fail "the API server is not ready"
    wait_for 60 kubectl_admin get node "${HOSTNAME}" || fail "the node did not register"
    [ "$(kubectl_admin get node "${HOSTNAME}" -o jsonpath='{.status.nodeInfo.kubeletVersion}')" = "${version}" ] ||
        fail "the kubelet of the node is not ${version}"
    pass "the control plane is up"

    echo "--- ${version}: kubeadm reset and uninstall"
    on_host kubeadm reset --force >"${WORK}/kubeadm-reset.log" 2>&1 ||
        { cat "${WORK}/kubeadm-reset.log" >&2; fail "kubeadm reset failed"; }
    run_script "${uninstall}" || fail "the uninstall script failed"
    for pkg in kubelet kubeadm kubectl kubernetes-cni cri-tools; do
        ! is_installed "${pkg}" || fail "${pkg} is left"
    done
    for path in /usr/local/bin/containerd /usr/local/bin/ctr /usr/local/sbin/runc \
        /usr/local/lib/systemd/system/containerd.service /etc/containerd /var/lib/containerd /run/containerd \
        /etc/apt/sources.list.d/byoh-kubernetes.sources /etc/apt/keyrings/byoh-kubernetes.gpg /etc/crictl.yaml \
        /etc/modules-load.d/byoh-k8s.conf /etc/sysctl.d/99-byoh-k8s.conf; do
        ! on_host test -e "${path}" || fail "${path} is left"
    done
    ! on_host pgrep -x 'containerd|containerd-shim.*|kubelet' >/dev/null || fail "a process of containerd or the kubelet is left"
    pass "removed"
done

echo "PASS"
