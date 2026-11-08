# How to test the Bring Your Own Host Provider locally

This doc provides instructions about how to test Bring Your Own Host Provider on a local workstation using:

- [Kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) for provisioning a management cluster
- [Docker](https://docs.docker.com/engine/install/) run for creating hosts to be used as a capacity for BYO Host machines
- [BYOH](https://github.com/superbrothers/cluster-api-provider-bringyourownhost) provider to add the above hosts to the aforemention workload cluster
- [Tilt](https://docs.tilt.dev/install.html) for faster iterative development

## Pre-requisites

It is required to have a docker image to be used when doing docker run for creating hosts

__Clone BYOH Repo__
```shell
git clone git@github.com:superbrothers/cluster-api-provider-bringyourownhost.git
```

## Setting up the management cluster

### Creates the Kubernetes cluster

We are using [kind](https://kind.sigs.k8s.io/) to create the Kubernetes cluster that will be turned into a Cluster API management cluster later in this doc.

```shell
cat > kind-cluster.yaml <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  image: kindest/node:v1.36.4@sha256:099e049362a1526b2db71494e1947aae99bd16290d7c895f2b7ea312e3cbfaed
EOF

kind create cluster --config kind-cluster.yaml
```

### Installing Cluster API

Installing cluster API into the Kubernetes cluster will turn it into a Cluster API management cluster.

We are going using [tilt](https://tilt.dev/) in order to do so, so you can have your local environment set up for rapid iterations, as described in
[Developing Cluster API with Tilt](https://cluster-api.sigs.k8s.io/developer/core/tilt).

In order to do so you need to clone both https://github.com/kubernetes-sigs/cluster-api/ and https://github.com/superbrothers/cluster-api-provider-bringyourownhost locally;

__Clone CAPI Repo__

```shell
git clone git@github.com:kubernetes-sigs/cluster-api.git
cd cluster-api
git checkout v1.14.3
```

__Create a tilt-settings.json file__

Next, create a tilt-settings.json file and place it in your local copy of cluster-api:  

```shell
cat > tilt-settings.json <<EOF
{
  "default_registry": "ghcr.io/<your-github-account>",
  "enable_providers": ["byoh", "kubeadm-bootstrap", "kubeadm-control-plane"],
  "provider_repos": ["../cluster-api-provider-bringyourownhost"]
}
EOF
```

`default_registry` is the registry that Tilt pushes the images to, so set one that you can push to. Do not set `ghcr.io/superbrothers`, where the released images are. The Tiltfile of Cluster API does not need it when the kind cluster has a local registry, as described in [Developing Cluster API with Tilt](https://cluster-api.sigs.k8s.io/developer/core/tilt).

__Run Tilt__

To launch your development environment, run below command and keep it running in the shell

```shell
tilt up
```
Wait for all the resources to come up, status can be viewed in Tilt UI.
## Creating the workload cluster

Now that you have a management cluster with Cluster API and BYOHost provider installed, we can start to create a workload
cluster.

### Generating the Bootstrap Kubeconfig file
Get the APIServer and Certificate Authority Data info

```shell
APISERVER=$(kubectl config view -ojsonpath='{.clusters[0].cluster.server}')
CA_CERT=$(kubectl config view --flatten -ojsonpath='{.clusters[0].cluster.certificate-authority-data}')
```

Each host needs its own bootstrap kubeconfig, and it must be used within 30 minutes, as the bootstrap token in it expires then. See [Host registration and permissions](host_registration.md) for how the hosts join the management cluster and what they can do there.

Create a BootstrapKubeconfig CR for each of the n hosts, where ```n>1```, as follows
```shell
for i in {1..n}
do
cat <<EOF | kubectl apply -f -
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: BootstrapKubeconfig
metadata:
  name: bootstrap-kubeconfig-host$i
  namespace: default
spec:
  apiserver: "$APISERVER"
  certificate-authority-data: "$CA_CERT"
EOF
done
```

Once the BootstrapKubeconfig CRs are created, fetch the bootstrap kubeconfig files from their Status field
```shell
for i in {1..n}
do
kubectl get bootstrapkubeconfig bootstrap-kubeconfig-host$i -n default -o=jsonpath='{.status.bootstrapKubeconfigData}' > ~/bootstrap-kubeconfig-host$i.conf
done
```


### Add a minimum of two hosts to the capacity pool

Create Management Cluster kubeconfig
```shell
export KIND_IP=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' kind-control-plane)
sed -i 's/    server\:.*/    server\: https\:\/\/'"$KIND_IP"'\:6443/g' ~/bootstrap-kubeconfig-host*.conf
```
Generate host-agent binaries
```
make host-agent-binaries
```

### Create docker hosts
```shell
cd cluster-api-provider-bringyourownhost
make prepare-byoh-docker-host-image-dev
```
Run the following to create n hosts, where ```n>1```
```shell
for i in {1..n}
do
echo "Creating docker container host $i"
docker run --detach --tty --hostname host$i --name host$i --privileged --security-opt seccomp=unconfined --tmpfs /tmp --tmpfs /run --volume /var --volume /lib/modules:/lib/modules:ro --network kind byoh/node:v1.22.3
echo "Copy agent binary to host $i"
docker cp bin/byoh-hostagent-linux-amd64 host$i:/byoh-hostagent
echo "Copy kubeconfig to host $i"
docker cp ~/bootstrap-kubeconfig-host$i.conf host$i:/bootstrap-kubeconfig.conf
done
```

Start the host agent on the host and keep it running

```shell
docker exec -it $HOST_NAME bin/bash

./byoh-hostagent --bootstrap-kubeconfig bootstrap-kubeconfig.conf --label role=control-plane
```

Repeat the same steps with by changing the `HOST_NAME` env variable for all the hosts that you created. Start the agent of one host with `--label role=control-plane`, and the others with `--label role=worker`, as the cluster templates select the hosts by the label of the role. Use the IP of the host of the control plane as `CONTROL_PLANE_ENDPOINT_IP` below.

Check if the hosts registered itself into the management cluster.

Open another shell and run
```shell
kubectl get byohosts 
```

Open a new shell and change directory to `cluster-api-provider-bringyourownhost` repository. Run below commands

```shell
export CLUSTER_NAME="test1"
export NAMESPACE="default"
export KUBERNETES_VERSION="v1.22.3"
export CONTROL_PLANE_MACHINE_COUNT=1
export WORKER_MACHINE_COUNT=1
export CONTROL_PLANE_ENDPOINT_IP=<IP of the host that you start with --label role=control-plane>
```

From ```cluster-api-provider-bringyourownhost``` folder

```shell
cat test/e2e/data/infrastructure-provider-byoh/v1beta1/templates/docker/cluster-template.yaml | envsubst | kubectl apply -f -
```

```shell
kubectl get machines 
```

Dig into host when machine gets provisioned.

```shell
kubectl get kubeadmconfig

kubectl get BYOmachines  

kubectl get BYOhost 
```

Deploy a CNI solution

```shell
kubectl get secret $CLUSTER_NAME-kubeconfig -o jsonpath='{.data.value}' | base64 -d > $CLUSTER_NAME-kubeconfig
kubectl --kubeconfig $CLUSTER_NAME-kubeconfig apply -f test/e2e/data/cni/kindnet/kindnet.yaml
```

After a short while, our nodes should be running and in Ready state.
Check the workload cluster

```shell
kubectl --kubeconfig $CLUSTER_NAME-kubeconfig get nodes
```

Or peek at the host agent logs.
## Cleanup

```shell
kubectl delete cluster $CLUSTER_NAME
docker rm -f $HOST_NAME
kind delete cluster
```

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
The installer provides the scripts that install and uninstall the Kubernetes components on the hosts. See [installer](./installer.md) for the supported OSes and how it works.

## Pre-requisites
As of writing this, the following packages must be pre-installed on the BYOH host:
- socat
- ebtables
- ethtool
- conntrack
```shell
sudo apt-get install socat ebtables ethtool conntrack
```
