#!/usr/bin/env bash
# Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
# SPDX-License-Identifier: Apache-2.0

# Tests the deb package of the host agent: installs, configures, upgrades,
# removes and purges it, and checks the unit and the maintainer scripts.
#
# Usage: sudo hack/test-host-agent-deb.sh path/to/byoh-hostagent_<version>_<arch>.deb
#
# It changes the system: run it only on a throwaway machine with systemd, such
# as a runner of GitHub Actions or a container. It refuses to run on a host
# that has the agent installed or configured.

set -o errexit
set -o nounset
set -o pipefail

DEB=$(realpath "$1")
UNIT=byoh-hostagent.service
UNKNOWN_FLAG=--byoh-deb-test-unknown-flag
DROP_IN=/run/systemd/system/${UNIT}.d/byoh-deb-test.conf

fail() {
    echo "FAIL: $*" >&2
    systemctl status --no-pager "${UNIT}" >&2 || true
    journalctl --no-pager -u "${UNIT}" -n 50 >&2 || true
    exit 1
}

pass() {
    echo "ok: $*"
}

# wait_for runs the command until it succeeds, for up to 60 seconds.
wait_for() {
    local _
    for _ in $(seq 60); do
        if "$@"; then
            return 0
        fi
        sleep 1
    done
    return 1
}

unit_property() {
    systemctl show --value -p "$1" "${UNIT}"
}

is_active() {
    systemctl is-active "${UNIT}" || true
}

journal_has() {
    journalctl --no-pager -o cat -u "${UNIT}" | grep -qF -- "$1"
}

restarted() {
    [ "$(unit_property NRestarts)" -ge 1 ]
}

# package_state prints the state of the package in dpkg, such as "ii" when it
# is installed and "rc" when only its configuration is left, or nothing when
# dpkg does not know it.
package_state() {
    dpkg-query -W -f '${db:Status-Abbrev}' byoh-hostagent 2>/dev/null | tr -d ' ' || true
}

apt_get() {
    DEBIAN_FRONTEND=noninteractive apt-get -qq -y -o Dpkg::Use-Pty=0 "$@"
}

[ "$(id -u)" = 0 ] || fail "run as root"
[ -d /run/systemd/system ] || fail "systemd is not running"
for path in /etc/byoh-hostagent /etc/default/byoh-hostagent /var/lib/byoh-hostagent /root/.byoh; do
    [ ! -e "${path}" ] || fail "${path} exists; run this test only on a throwaway machine"
done
case "$(package_state)" in
    ""|un|pn) ;;
    *) fail "dpkg knows byoh-hostagent; run this test only on a throwaway machine" ;;
esac

echo "--- Install"
apt_get install "${DEB}"
[ "$(package_state)" = "ii" ] || fail "the package is not installed"
[ "$(stat -c %a:%U /usr/bin/byoh-hostagent)" = "755:root" ] || fail "the mode of /usr/bin/byoh-hostagent"
[ "$(stat -c %a:%U /etc/byoh-hostagent)" = "700:root" ] || fail "the mode of /etc/byoh-hostagent"
dpkg-query --control-show byoh-hostagent conffiles | grep -qx '/etc/default/byoh-hostagent' ||
    fail "/etc/default/byoh-hostagent is not a conffile"
/usr/bin/byoh-hostagent --version | grep -qF 'byoh-hostagent version' || fail "byoh-hostagent --version"
systemd-analyze verify "/usr/lib/systemd/system/${UNIT}" || fail "systemd-analyze verify"
[ "$(systemctl is-enabled "${UNIT}" || true)" = "disabled" ] || fail "the unit is enabled"
[ "$(is_active)" = "inactive" ] || fail "the unit is started"
pass "installed, and the unit is neither enabled nor started"

echo "--- Without configuration, the unit does not start and does not fail"
systemctl start "${UNIT}"
[ "$(unit_property ConditionResult)" = "no" ] || fail "the conditions did not skip the start"
[ "$(is_active)" = "inactive" ] || fail "the unit is not inactive"
pass "skipped by the conditions"

echo "--- With a bootstrap kubeconfig, the agent runs with the flags in /etc/default/byoh-hostagent and keeps restarting"
cat > /etc/byoh-hostagent/bootstrap-kubeconfig <<'EOF'
apiVersion: v1
kind: Config
clusters:
- cluster:
    insecure-skip-tls-verify: true
    server: https://127.0.0.1:9
  name: test
contexts:
- context:
    cluster: test
    user: test
  name: test
current-context: test
users:
- name: test
  user:
    token: abcdef.0123456789abcdef
EOF
chmod 0600 /etc/byoh-hostagent/bootstrap-kubeconfig
# An unknown flag after another one shows that the variable is split into
# separate arguments.
sed -i "s|^BYOH_HOSTAGENT_ARGS=.*|BYOH_HOSTAGENT_ARGS=\"--namespace=byoh-deb-test ${UNKNOWN_FLAG}\"|" /etc/default/byoh-hostagent
systemctl enable --now "${UNIT}"
[ "$(unit_property ConditionResult)" = "yes" ] || fail "the conditions skipped the start"
wait_for journal_has "unknown flag: ${UNKNOWN_FLAG}" || fail "the flags did not reach the agent"
wait_for restarted || fail "the agent did not restart"
pass "the flags reached the agent as separate arguments, and the agent restarted"
sed -i 's|^BYOH_HOSTAGENT_ARGS=.*|BYOH_HOSTAGENT_ARGS="--namespace=byoh-deb-test --label=role=worker"|' /etc/default/byoh-hostagent
systemctl restart "${UNIT}"
wait_for journal_has "initiated bootstrap kubeconfig flow" || fail "the agent did not start the bootstrap flow"
pass "the agent started the bootstrap flow with the bootstrap kubeconfig"

echo "--- With only the kubeconfig of a registered host, the unit starts"
rm /etc/byoh-hostagent/bootstrap-kubeconfig
mkdir -p /root/.byoh
echo "not a kubeconfig" > /root/.byoh/config
systemctl restart "${UNIT}"
[ "$(unit_property ConditionResult)" = "yes" ] || fail "the conditions skipped the start"
pass "started"

echo "--- An upgrade restarts the running agent and keeps it enabled"
# The agent exits at once without a management cluster, so run a process that
# does not exit instead, to tell a restart by the upgrade from the restarts of
# the agent.
mkdir -p "$(dirname "${DROP_IN}")"
printf '[Service]\nExecStart=\nExecStart=/bin/sleep infinity\n' > "${DROP_IN}"
systemctl daemon-reload
systemctl restart "${UNIT}"
pid=$(unit_property MainPID)
[ "${pid}" != 0 ] || fail "the unit is not running"
apt_get install --reinstall "${DEB}"
[ "$(systemctl is-enabled "${UNIT}")" = "enabled" ] || fail "the upgrade changed the enablement"
[ "$(is_active)" = "active" ] || fail "the unit is not running after the upgrade"
[ "$(unit_property MainPID)" != "${pid}" ] || fail "the upgrade did not restart the unit"
pid=$(unit_property MainPID)
pass "restarted, and the unit is enabled"

echo "--- Remove stops the agent and keeps the configuration and the credentials"
echo "not a kubeconfig" > /etc/byoh-hostagent/bootstrap-kubeconfig
echo "not a key" > /root/.byoh/byoh-deb-test.key
echo "not a key" > /var/lib/byoh-hostagent/byoh-client.key.tmp
apt_get remove byoh-hostagent
[ "$(package_state)" = "rc" ] || fail "the package is not removed"
[ "$(is_active)" = "inactive" ] || fail "the unit is running"
! kill -0 "${pid}" 2>/dev/null || fail "the process of the unit is left"
[ ! -e /usr/bin/byoh-hostagent ] || fail "/usr/bin/byoh-hostagent is left"
for path in /etc/default/byoh-hostagent /etc/byoh-hostagent/bootstrap-kubeconfig /root/.byoh/config; do
    [ -e "${path}" ] || fail "${path} was removed"
done
pass "stopped and removed, and the configuration and the credentials are kept"

echo "--- Purge removes the configuration, the state and the credentials"
apt_get purge byoh-hostagent
case "$(package_state)" in
    ""|un|pn) ;;
    *) fail "the package is not purged: $(package_state)" ;;
esac
for path in /etc/default/byoh-hostagent /etc/byoh-hostagent /var/lib/byoh-hostagent /root/.byoh \
    "/etc/systemd/system/multi-user.target.wants/${UNIT}"; do
    if [ -e "${path}" ] || [ -L "${path}" ]; then
        fail "${path} is left"
    fi
done
pass "purged"

rm -rf "$(dirname "${DROP_IN}")"
systemctl daemon-reload
echo "PASS"
