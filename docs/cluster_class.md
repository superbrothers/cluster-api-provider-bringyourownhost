# ClusterClass and Managed Topologies in BYOH

_ClusterClass and Managed Topologies_ is a feature introduced by the [Cluster API community](https://cluster-api.sigs.k8s.io/) to simply the provisioning, upgrade, and operate multiple Kubernetes clusters in a declarative way.

### ClusterClass

ClusterClass let you define the shape of your cluster once, and reuse it many times, abstracting the complexities and the internals of a Kubernetes cluster away.

In general, it contains the collection of Cluster and Machine templates which can be leveraged to create multiple clusters of similar shape.
![Cluster-class](./diagrams/clusterclass.jpg)

For instance, in BYOH we have defined one ClusterClass named `quickstart`

```yaml
apiVersion: cluster.x-k8s.io/v1beta1
kind: ClusterClass
metadata:
  name: quickstart
spec:
  controlPlane:
    ref:
      apiVersion: controlplane.cluster.x-k8s.io/v1beta1
      kind: KubeadmControlPlaneTemplate
      name: quickstart-control-plane
    machineInfrastructure:
      ref:
        kind: ByoMachineTemplate
        apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
        name: quickstart-control-plane-machine
  infrastructure:
    ref:
      apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
      kind: ByoClusterTemplate
      name: quickstart-cluster
  workers:
    machineDeployments:
      - class: quickstart-worker
        strategy:
          type: RollingUpdate
          rollingUpdate:
            maxSurge: 0
            maxUnavailable: 1
        template:
          bootstrap:
            ref:
              apiVersion: bootstrap.cluster.x-k8s.io/v1beta1
              kind: KubeadmConfigTemplate
              name: quickstart-worker-bootstrap-template
          infrastructure:
            ref:
              apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
              kind: ByoMachineTemplate
              name: quickstart-worker-machinetemplate
  variables:
    - name: controlPlaneIpAddr
      required: true
      schema:
        openAPIV3Schema:
          type: string
    - name: controlPlanePort
      required: false
      schema:
        openAPIV3Schema:
          description: Port of the control plane endpoint.
          type: integer
          default: 6443
    - name: controlPlaneMaxSurge
      required: false
      schema:
        openAPIV3Schema:
          description: maxSurge of the rollout of the control plane. 0 needs 3 or more control plane machines.
          type: integer
          minimum: 0
          maximum: 1
          default: 1
  patches:
    - name: controlPlaneEndpoint
      description: "Sets control plane endpoint"
      definitions:
        - selector:
            apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
            kind: ByoClusterTemplate
            matchResources:
              infrastructureCluster: true
          jsonPatches:
            - op: add
              path: "/spec/template/spec/controlPlaneEndpoint"
              valueFrom:
                template: |
                  host: '{{ .controlPlaneIpAddr }}'
                  port: {{ .controlPlanePort }}
    - name: controlPlaneRolloutStrategy
      description: "Sets maxSurge of the rollout of the control plane"
      definitions:
        - selector:
            apiVersion: controlplane.cluster.x-k8s.io/v1beta1
            kind: KubeadmControlPlaneTemplate
            matchResources:
              controlPlane: true
          jsonPatches:
            - op: add
              path: /spec/template/spec/rolloutStrategy
              valueFrom:
                template: |
                  type: RollingUpdate
                  rollingUpdate:
                    maxSurge: {{ .controlPlaneMaxSurge }}
```

To generate a cluster using this ClusterClass, refer below yaml where we create just one `kind` named `Cluster`
```yaml
apiVersion: cluster.x-k8s.io/v1beta1
kind: Cluster
metadata:
  name: ${CLUSTER_NAME}
spec:
  clusterNetwork:
    services:
      cidrBlocks:
        - 10.128.0.0/12
    pods:
      cidrBlocks:
        - 192.168.0.0/16
    serviceDomain: cluster.local
  topology:
    class: quickstart
    version: ${KUBERNETES_VERSION}
    controlPlane:
      metadata: {}
      replicas: 1
    variables:
      - name: controlPlaneIpAddr
        value: ${CONTROL_PLANE_ENDPOINT_IP}
      - name: controlPlanePort
        value: ${CONTROL_PLANE_ENDPOINT_PORT:=6443}
      - name: controlPlaneMaxSurge
        value: ${CONTROL_PLANE_MAX_SURGE:=1}
    workers:
      machineDeployments:
        - class: quickstart-worker
          metadata: {}
          name: md-0
          replicas: 1
```

### How to use with clusterctl?

To create a cluster using clusterctl with managed topology, use `topology` flavor and set following environment variables :
```
CLUSTER_NAME
CONTROL_PLANE_ENDPOINT_IP
```
The following variables are optional:
- `CONTROL_PLANE_ENDPOINT_PORT`: the port of the control plane endpoint (default `6443`).
- `CONTROL_PLANE_MAX_SURGE`: `maxSurge` of the rollout of the control plane (default `1`). Set it to `0` to update the control plane without a spare host, which needs 3 or more control plane machines.

The ByoMachineTemplates of the ClusterClass select the hosts by the label `role`: `role=control-plane` for the control plane, and `role=worker` for the workers. Start the agents with `--label role=control-plane` or `--label role=worker`.

Also set feature flag for ClusterClass:

**Feature gate name:** `ClusterTopology`

**Variable name to enable/disable the feature gate**: `CLUSTER_TOPOLOGY`

After setting the variables use below command to create a yaml file to create a cluster with managed topology
```shell
clusterctl generate cluster <cluster-name> --kubernetes-version <kubernetes-version> --control-plane-machine-count=3 --worker-machine-count=3 \
    --flavor topology > my-cluster.yaml
```

#### clusterctl alpha topology plan

To preview  the Cluster generated using such ClusterClass, use `clusterctl alpha topology plan`

```shell
clusterctl alpha topology plan -f example-cluster-class.yaml -f example-cluster.yaml -o output/
```

To know more about clusterClass, refer to [this](https://cluster-api.sigs.k8s.io/tasks/experimental-features/cluster-class/index.html) documentation.