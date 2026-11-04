// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// byoHostRole is the role of a BYO host. The agent labels the ByoHost with
// role=<role>, and the cluster templates select the hosts of the control plane
// and of the workers by the label.
type byoHostRole string

const (
	controlPlaneRole byoHostRole = "control-plane"
	workerRole       byoHostRole = "worker"

	// byoHostNetwork is the docker network of the kind cluster, which the
	// hosts and the load balancer join.
	byoHostNetwork = "kind"
)

// byoHost is a BYO host for a spec to create.
type byoHost struct {
	Name string
	Role byoHostRole
}

// byoHosts is the BYO hosts of a spec, and the load balancer in front of the
// API servers of the control plane hosts.
type byoHosts struct {
	dockerClient *client.Client
	// hostContainers and loadBalancer are the names of the containers. They
	// are recorded before the containers are created, so that the cleanup
	// also removes the containers that a failure leaves halfway.
	hostContainers []string
	loadBalancer   string
	// controlPlaneEndpointIP is the IP of the load balancer, which the cluster
	// templates take from CONTROL_PLANE_ENDPOINT_IP.
	controlPlaneEndpointIP string
	logFiles               []string
	outputs                []types.HijackedResponse
	files                  []*os.File
}

// newByoHosts returns the BYO hosts of a spec, which Start creates. Call it in
// BeforeEach. It registers the cleanup of the hosts with DeferCleanup, which
// runs after the AfterEach nodes delete the cluster, even if they fail: the
// agents reset the hosts while the cluster is deleted, and the cluster reaches
// its API servers through the load balancer until then.
func newByoHosts(ctx context.Context) *byoHosts {
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	Expect(err).NotTo(HaveOccurred())
	h := &byoHosts{dockerClient: dockerClient}
	// The cleanup has to run even if the context of the spec is canceled.
	cleanupCtx := context.WithoutCancel(ctx)
	DeferCleanup(func() { h.cleanup(cleanupCtx) })
	return h
}

// Start creates the hosts, runs the agent on each of them in the namespace with
// the label of its role, and starts the load balancer in front of the control
// plane hosts. It sets the IP of the load balancer to
// CONTROL_PLANE_ENDPOINT_IP for the cluster templates.
func (h *byoHosts) Start(ctx context.Context, namespace string, hosts []byoHost) {
	Expect(h.loadBalancer).To(BeEmpty(), "the hosts are already started")
	var controlPlaneHosts []string
	for _, host := range hosts {
		Expect(host.Role).To(BeElementOf(controlPlaneRole, workerRole), "the host %s has an unknown role", host.Name)
		if host.Role == controlPlaneRole {
			controlPlaneHosts = append(controlPlaneHosts, host.Name)
		}
	}
	Expect(controlPlaneHosts).NotTo(BeEmpty(), "no control plane hosts for the load balancer")

	for _, host := range hosts {
		h.startHost(ctx, namespace, host)
	}

	// Pass all the control plane hosts: the load balancer sends the requests
	// only to the hosts whose API servers are healthy.
	h.loadBalancer = namespace + "-lb"
	StartControlPlaneLoadBalancer(ctx, h.dockerClient, byoHostNetwork, h.loadBalancer, controlPlaneHosts)
	h.controlPlaneEndpointIP = os.Getenv(controlPlaneEndpointIPVariable)
}

// startHost creates the host and runs the agent on it, writing the log of the
// agent to a file.
func (h *byoHosts) startHost(ctx context.Context, namespace string, host byoHost) {
	runner := ByoHostRunner{
		Context:               ctx,
		clusterConName:        clusterConName,
		ByoHostName:           host.Name,
		Namespace:             namespace,
		PathToHostAgentBinary: pathToHostAgentBinary,
		DockerClient:          h.dockerClient,
		NetworkInterface:      byoHostNetwork,
		bootstrapClusterProxy: bootstrapClusterProxy,
		CommandArgs: map[string]string{
			"--bootstrap-kubeconfig": "/bootstrap.conf",
			"--namespace":            namespace,
			"--v":                    "1",
			"--label":                "role=" + string(host.Role),
		},
		BootstrapKubeconfigData: generateBootstrapKubeconfig(ctx, bootstrapClusterProxy, clusterConName),
	}
	// The container is named after the host.
	h.hostContainers = append(h.hostContainers, host.Name)
	byohost, err := runner.SetupByoDockerHost()
	Expect(err).NotTo(HaveOccurred())
	output, _, err := runner.ExecByoDockerHost(byohost)
	Expect(err).NotTo(HaveOccurred())
	h.outputs = append(h.outputs, output)

	logFile := fmt.Sprintf("/tmp/host-agent-%s.log", host.Name)
	h.logFiles = append(h.logFiles, logFile)
	if f := WriteDockerLog(output, logFile); f != nil {
		h.files = append(h.files, f)
	}
}

// LogFiles returns the files of the logs of the agents. It returns nil if
// newByoHosts has not been called.
func (h *byoHosts) LogFiles() []string {
	if h == nil {
		return nil
	}
	return h.logFiles
}

// ControlPlaneEndpointIP returns the IP of the load balancer.
func (h *byoHosts) ControlPlaneEndpointIP() string {
	return h.controlPlaneEndpointIP
}

// cleanup removes the containers of the hosts and of the load balancer, and the
// logs of the agents. It goes on after an error, so that it leaves as little as
// possible, and fails the spec with all the errors at the end.
func (h *byoHosts) cleanup(ctx context.Context) {
	errs := h.removeContainers(ctx)
	h.removeLogs()
	if err := h.dockerClient.Close(); err != nil {
		Showf("error closing the docker client: %v", err)
	}
	Expect(errors.Join(errs...)).NotTo(HaveOccurred())
}

// removeContainers removes the containers of the hosts and of the load
// balancer that exist.
func (h *byoHosts) removeContainers(ctx context.Context) []error {
	var errs []error
	for _, name := range h.hostContainers {
		if err := h.dockerClient.ContainerStop(ctx, name, container.StopOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("stopping the host %s: %w", name, err))
		}
		// The hosts have an anonymous volume on /var.
		if err := h.dockerClient.ContainerRemove(ctx, name, container.RemoveOptions{RemoveVolumes: true}); err != nil && !cerrdefs.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("removing the host %s: %w", name, err))
		}
	}
	if h.loadBalancer == "" {
		return errs
	}
	if err := h.dockerClient.ContainerRemove(ctx, h.loadBalancer, container.RemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
		errs = append(errs, fmt.Errorf("removing the load balancer %s: %w", h.loadBalancer, err))
	}
	// The IP is not for the later specs, which start their own load balancers
	// or choose the IP themselves.
	if err := os.Unsetenv(controlPlaneEndpointIPVariable); err != nil {
		errs = append(errs, fmt.Errorf("unsetting %s: %w", controlPlaneEndpointIPVariable, err))
	}
	return errs
}

// removeLogs stops writing the logs of the agents, and removes the files.
func (h *byoHosts) removeLogs() {
	for i := range h.outputs {
		h.outputs[i].Close()
	}
	for _, f := range h.files {
		if err := f.Close(); err != nil {
			Showf("error closing file %s: %v", f.Name(), err)
		}
	}
	for _, logFile := range h.logFiles {
		if err := os.Remove(logFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
			Showf("error removing file %s: %v", logFile, err)
		}
	}
}
