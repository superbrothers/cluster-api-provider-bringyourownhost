# Troubleshooting Tips for Kubernetes Cluster API Provider Bring Your Own Host (BYOH)
This section includes tips to help you to troubleshoot common problems that you might encounter when installing Kubernetes Cluster API Provider BYOH.

## No k8s support for OS
### Problem
The default installer does not support the OS or the architecture of the host. The manager logs `failed to create installer instance` with the error `No k8s support for OS`, and the K8sInstallerConfig does not become ready, so the ByoMachine waits for it.
### Solution
Check the OSes and the architectures that the default installer supports in [installer](./installer.md). For the other hosts, install the Kubernetes components on the host manually, run the agent with the `--skip-installation` flag, and do not set `installerRef` in the ByoMachineTemplate. You can also implement a custom installer.

## Github rate-limiting issue during clusterctl init
### Problem
During `clusterctl init -i byoh`, sometimes we might face github rate limit error and unable to pull providers.
### Solution
To fix it set environment variable `GITHUB_TOKEN` and fetch its value from github. To create new `GITHUB_TOKEN` refer [this doc](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/creating-a-personal-access-token).
