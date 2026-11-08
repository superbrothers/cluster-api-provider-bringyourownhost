# Kubernetes Cluster API Provider Bring Your Own Host (BYOH)

This is a maintained fork of [vmware-tanzu/cluster-api-provider-bringyourownhost](https://github.com/vmware-tanzu/cluster-api-provider-bringyourownhost), which is no longer maintained ([vmware-tanzu/cluster-api-provider-bringyourownhost#894](https://github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/issues/894)).

## What is Cluster API Provider BYOH

[Cluster API](https://github.com/kubernetes-sigs/cluster-api) brings
declarative, Kubernetes-style APIs to cluster creation, configuration and
management.

__BYOH__ is a Cluster API Infrastructure Provider for already-provisioned hosts running Linux. This provider allows operators to adopt Cluster API for deploying and managing kubernetes nodes without also having to adopt a specific infrastructure service. This enables users to decouple kubernetes node provisioning from host and infrastructure provisioning.

## BYOH Glossary
**Host** - A host is a running computer system. It could be physical or virtual. It has a kernel and some base operating system

**BYO Host** - A Linux host provisioned and managed outside of Cluster API

**BYOH Capacity Pool** - A set of BYO Hosts registered in a namespace of a management cluster & authorized for usage as a capacity for deploying Kubernetes nodes of the clusters in the same namespace

**Kubernetes Node** - A Kubernetes Node that runs on top of a Host. There is a 1-to-1 relationship between nodes and hosts (every host has zero or one nodes). Node provisioning and lifecycle management is a Cluster API responsibility

**Kubernetes Host Components** - The components that run uncontainerized on the host and are required to bootstrap a Kubernetes node. Typically, this is at least kubelet, containerd and kubeadm, but different OS might require different components in this category

## Features

- Native Kubernetes manifests and API
- Support for single and multi-node control plane clusters
- Support already provisioned Linux hosts with Ubuntu 24.04 and 26.04

## Getting Started
Check out the [getting_started](docs/getting_started.md) guide for launching a BYOH workload cluster

## Host Registration and Permissions
Check out [host registration and permissions](docs/host_registration.md) for how the hosts join the management cluster, what they can do there, and how to operate them securely.

## Project Status

This project is currently a work-in-progress, in an Alpha state, so it may not be production ready. There is no backwards-compatibility guarantee at this point. For more details on the roadmap and upcoming features, check out the project's [issue](https://github.com/superbrothers/cluster-api-provider-bringyourownhost/issues) tracker on GitHub.

## Getting involved and contributing

### Launching a Kubernetes cluster using BYOH source code

Check out the [developer guide](./docs/local_dev.md) for launching a BYOH cluster consisting of Docker containers as hosts.

## Implement Custom Installer controller
An installer controller is responsible to provide the installation and uninstallation scripts for k8s dependencies, prerequisites and components on each `BYOHost`.  
If someone wants to implement their own installer controller then they need to follow the contract defined in [installer](./docs/installer.md) doc.

------

## Compatibility with Cluster API

- BYOH implements the Cluster API v1beta1 contract, and is built and tested against Cluster API v1.14

## Supported OS and architectures
| Operating System  | Architecture  |
| ------------------|---------------|
| Ubuntu 24.04.*    | amd64, arm64  |
| Ubuntu 26.04.*    | amd64, arm64  |

**NOTE:**  The '*' in OS means that all the point releases are supported.

## Tested Kubernetes versions
The default installer installs the version of Kubernetes of the Machine from pkgs.k8s.io (see [installer](./docs/installer.md)). The following minor releases, which the Kubernetes project maintains (https://kubernetes.io/releases/), have been tested.

| Kubernetes | Tested patch release |
| -----------|----------------------|
| v1.37      | v1.37.2              |
| v1.36      | v1.36.6              |
| v1.35      | v1.35.10             |

**NOTE:**  The tests ran on amd64 hosts of the supported OSes with the default versions of containerd (2.3.7) and runc (1.5.2): the default installer installs the version, `kubeadm init` brings up a control plane, and the uninstall script removes it. arm64 has not been tested yet.
