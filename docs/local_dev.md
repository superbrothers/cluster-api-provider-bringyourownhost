# Local development

Test the changes locally with the end-to-end tests below, which create a management cluster with kind and the hosts in Docker containers from the source. To try the released provider by hand, follow [Getting started](getting_started.md).

# End-to-end tests
The end-to-end tests create a kind management cluster, the hosts in containers on the `kind` network, and a load balancer for the control plane of each workload cluster.

The hosts are privileged containers that share the kernel of the machine that runs them:
- Raise the kernel limits on the machine before running the tests; the tests stop when they are lower.
  - kube-proxy in the hosts cannot raise the conntrack limit from a container. It needs `max(32768 * CPUs, 131072)`.
  - systemd, containerd and the kubelet in each host use the inotify instances and watches of root on the machine. The kubelet fails with `inotify_init: too many open files` when they run out. The values are the ones that [kind recommends](https://kind.sigs.k8s.io/docs/user/known-issues/#pod-errors-due-to-too-many-open-files).
  ```shell
  sudo sysctl -w net.netfilter.nf_conntrack_max=$(( $(nproc) * 32768 > 131072 ? $(nproc) * 32768 : 131072 ))
  sudo sysctl -w fs.inotify.max_user_instances=512 fs.inotify.max_user_watches=524288
  ```
- The installer loads the kernel modules `overlay` and `br_netfilter` into the kernel of the machine.
- `swapoff` and `swapon` do nothing in the hosts, so the tests do not turn off the swap of the machine.

Run `[PR-Blocking]`, the spec that the pull requests run, which takes a few minutes:
```shell
make test-e2e-pr-blocking
```

Run the other specs only when a change needs them, as all of them take more than 20 minutes. `GINKGO_FOCUS` selects the specs by their names or labels, and `make test-e2e` without it runs all of them:
```shell
GINKGO_FOCUS="\[K8s-upgrade\]" make test-e2e
```

On GitHub Actions, the `e2e-suite` workflow runs `[PR-Blocking]` on amd64 for the pull requests that are not drafts, and all the specs on amd64 and arm64 every week. Run it from the Actions tab or with `gh workflow run e2e.yml --ref <branch>` to run all the specs on a branch, or the specs that the input `focus` matches.

# Host Agent Installer
The installer provides the scripts that install and uninstall the Kubernetes components on the hosts. See [installer](./installer.md) for the supported OSes and how it works. The hosts need no packages installed beforehand.
