// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"text/template"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	. "github.com/onsi/gomega" //nolint:staticcheck
)

const (
	// loadBalancerImage is the HAProxy image that CAPD uses for the load
	// balancer in front of the control plane.
	loadBalancerImage = "kindest/haproxy:v20230606-42a2262b"
	// loadBalancerConfigPath is the path to the config file in the image.
	loadBalancerConfigPath = "/usr/local/etc/haproxy/haproxy.cfg"
	// apiServerPort is the port of the API servers and of the load balancer.
	apiServerPort = 6443
	// controlPlaneEndpointIPVariable is the variable of the cluster templates
	// for the address of the control plane.
	controlPlaneEndpointIPVariable = "CONTROL_PLANE_ENDPOINT_IP"
)

// loadBalancerConfig is the HAProxy config of the load balancer, based on the
// one of CAPD (test/infrastructure/docker/internal/loadbalancer/config.go in
// sigs.k8s.io/cluster-api/test). The DNS of docker resolves the names of the
// hosts, and the load balancer starts before they run the API servers.
var loadBalancerConfig = template.Must(template.New("haproxy.cfg").Parse(`global
  log /dev/log local0
  log /dev/log local1 notice
  daemon
  # limit memory usage to approximately 18 MB
  maxconn 100000

resolvers docker
  nameserver dns 127.0.0.11:53

defaults
  log global
  mode tcp
  option dontlognull
  timeout connect 5000
  timeout client 50000
  timeout server 50000
  # allow to boot despite dns don't resolve backends
  default-server init-addr none

frontend control-plane
  bind *:{{ .Port }}
  default_backend kube-apiservers

backend kube-apiservers
  option httpchk GET /healthz
{{- range .Hosts }}
  server {{ . }} {{ . }}:{{ $.Port }} check check-ssl verify none resolvers docker resolve-prefer ipv4
{{- end }}
`))

// StartControlPlaneLoadBalancer starts a load balancer in front of the API
// servers of the given hosts on the docker network, and sets its IP to
// CONTROL_PLANE_ENDPOINT_IP for the cluster templates. BYOH does not provide
// load balancers, so this is only for the tests. Pass all the hosts that can
// become control plane machines: the load balancer sends the requests only to
// the hosts whose API servers are healthy. It returns the ID of the container,
// to be removed after the cluster is deleted.
func StartControlPlaneLoadBalancer(ctx context.Context, dockerClient *client.Client, networkName, name string, hosts []string) string {
	pull, err := dockerClient.ImagePull(ctx, loadBalancerImage, image.PullOptions{})
	Expect(err).NotTo(HaveOccurred())
	_, err = io.Copy(io.Discard, pull)
	Expect(err).NotTo(HaveOccurred())
	Expect(pull.Close()).To(Succeed())

	lb, err := dockerClient.ContainerCreate(ctx,
		&container.Config{Hostname: name, Image: loadBalancerImage},
		&container.HostConfig{NetworkMode: container.NetworkMode(networkName)},
		&network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{networkName: {}}},
		nil, name)
	Expect(err).NotTo(HaveOccurred())

	var config bytes.Buffer
	Expect(loadBalancerConfig.Execute(&config, struct {
		Port  int
		Hosts []string
	}{apiServerPort, hosts})).To(Succeed())
	dir, err := os.MkdirTemp("", "byoh-e2e-lb-")
	Expect(err).NotTo(HaveOccurred())
	defer func() {
		if removeErr := os.RemoveAll(dir); removeErr != nil {
			Showf("error removing %s: %v", dir, removeErr)
		}
	}()
	configPath := filepath.Join(dir, filepath.Base(loadBalancerConfigPath))
	Expect(os.WriteFile(configPath, config.Bytes(), 0o644)).To(Succeed()) //nolint:gosec,mnd
	Expect(copyToContainer(ctx, dockerClient, cpConfig{
		sourcePath: configPath,
		destPath:   loadBalancerConfigPath,
		container:  lb.ID,
	})).To(Succeed())

	Expect(dockerClient.ContainerStart(ctx, lb.ID, container.StartOptions{})).To(Succeed())
	inspect, err := dockerClient.ContainerInspect(ctx, lb.ID)
	Expect(err).NotTo(HaveOccurred())
	endpoint, ok := inspect.NetworkSettings.Networks[networkName]
	Expect(ok).To(BeTrue(), "the load balancer %s is not on the network %s", name, networkName)
	Expect(endpoint.IPAddress).NotTo(BeEmpty())
	Expect(os.Setenv(controlPlaneEndpointIPVariable, endpoint.IPAddress)).To(Succeed())
	return lb.ID
}
