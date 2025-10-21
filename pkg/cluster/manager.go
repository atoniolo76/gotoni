package cluster

import (
	"fmt"
	"strings"

	"toni/gpusnapshot/pkg/providers"
)

// Manager handles cluster operations
type Manager struct {
	// Future: add provider, config, etc.
}

// NewManager creates a new cluster manager
func NewManager() *Manager {
	return &Manager{}
}

// BuildK3sContainer creates a container configuration for k3s
func (m *Manager) BuildK3sContainer(version string, isServer bool, extraArgs []string) *providers.ContainerConfig {
	image := "rancher/k3s"
	if version != "latest" {
		// Remove 'v' prefix if present to avoid double 'v'
		cleanVersion := strings.TrimPrefix(version, "v")
		image = fmt.Sprintf("%s:v%s", image, cleanVersion)
	}

	// Build k3s command
	cmd := []string{}
	args := []string{}

	if isServer {
		cmd = append(cmd, "server")
	} else {
		cmd = append(cmd, "agent")
	}

	// Add extra arguments
	if len(extraArgs) > 0 {
		args = append(args, extraArgs...)
	}

	// Add common k3s flags for GPU/container workloads
	args = append(args, "--docker") // Use Docker instead of containerd for compatibility
	args = append(args, "--kubelet-arg=feature-gates=KubeletInUserNamespace=true")

	// Expose Kubernetes API port
	ports := []string{"6443/tcp"}
	if isServer {
		ports = append(ports, "8080/tcp", "10250/tcp") // Additional server ports
	}

	return &providers.ContainerConfig{
		Image:   image,
		Ports:   ports,
		Command: cmd,
		Args:    args,
	}
}

// LaunchInstance launches a new GPU instance with optional k3s
func (m *Manager) LaunchInstance(providerType, instanceType, region, name string, k3sConfig *K3sConfig) (*providers.Instance, error) {
	// Build launch request
	req := &providers.LaunchRequest{
		InstanceTypeName: instanceType,
		Region:          region,
		Name:            name,
	}

	// Add k3s container config if enabled
	if k3sConfig != nil && k3sConfig.Enabled {
		req.Container = m.BuildK3sContainer(k3sConfig.Version, k3sConfig.IsServer, k3sConfig.ExtraArgs)
	}

	// TODO: Get provider from factory and call LaunchInstance
	// provider, err := factory.NewProvider(providerType, apiToken)
	// if err != nil { return nil, err }
	// return provider.LaunchInstance(req)

	return nil, fmt.Errorf("provider integration not implemented yet - container config ready: %+v", req.Container)
}

// ListInstances lists all running instances
func (m *Manager) ListInstances(providerType string) ([]*providers.Instance, error) {
	// TODO: Implement instance listing
	// 1. Get provider from factory
	// 2. Call provider.ListInstances()

	return nil, fmt.Errorf("not implemented yet")
}

// TerminateInstance terminates a running instance
func (m *Manager) TerminateInstance(providerType, instanceID string) error {
	// TODO: Implement instance termination
	// 1. Get provider from factory
	// 2. Call provider.TerminateInstance()

	return fmt.Errorf("not implemented yet")
}

// K3sConfig holds k3s configuration options
type K3sConfig struct {
	Enabled bool
	Version string
	IsServer bool
	ExtraArgs []string
}

// NewK3sConfig creates a new k3s configuration
func NewK3sConfig(enabled bool, version string, isServer bool, extraArgs []string) *K3sConfig {
	return &K3sConfig{
		Enabled:   enabled,
		Version:   version,
		IsServer:  isServer,
		ExtraArgs: extraArgs,
	}
}
