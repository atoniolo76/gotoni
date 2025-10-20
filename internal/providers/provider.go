package providers

import (
	"fmt"
	"time"
)

// GPUType represents a specific Nvidia GPU model with its capabilities
type GPUType struct {
	Name          string  // e.g., "A100", "H100", "A10", "RTX_4090"
	Description   string  // e.g., "A100 (80 GB SXM4)"
	VRAM_GB       int     // GPU memory in GB
	CUDACores     int     // Number of CUDA cores
	ComputeCapability string // e.g., "8.0", "9.0"
	BaseClock_MHz int     // Base clock speed
	MemoryClock_MHz int   // Memory clock speed
	FP32_TFLOPS   float64 // Theoretical FP32 performance
}

// InstanceType represents an instance configuration with GPU specs
type InstanceType struct {
	Name        string    // Provider-specific instance type name
	Description string    // Human-readable description
	GPUs        []GPUType // GPUs in this instance type
	VCPUs       int       // Number of virtual CPUs
	RAM_GB      int       // RAM in GB
	Storage_GB  int       // Storage in GB
	PriceCentsPerHour int // Cost in cents per hour
	Regions     []string  // Available regions
}

// Instance represents a running GPU instance/pod
type Instance struct {
	ID           string
	Name         string
	Status       string // "running", "stopped", "terminated", etc.
	InstanceType InstanceType
	Region       string
	PublicIP     string
	PrivateIP    string
	SSHKeys      []string
	CreatedAt    time.Time
}

// ContainerConfig represents Docker container configuration
type ContainerConfig struct {
	Image       string            // Docker image name
	Environment map[string]string // Environment variables
	Ports       []string          // Ports to expose (e.g., ["22/tcp", "8888/http"])
	Command     []string          // Override container command
	Args        []string          // Override container args
}

// LaunchRequest represents parameters for launching a new instance
type LaunchRequest struct {
	InstanceTypeName string
	Region           string
	Name             string   // Optional instance name
	SSHKeyNames      []string // SSH keys to add
	Container        *ContainerConfig // Optional container configuration
}

// SSHKey represents an SSH key pair
type SSHKey struct {
	ID         string
	Name       string
	PublicKey  string
	PrivateKey string // Only returned when Lambda generates the key
}

// FirewallRule represents a firewall rule for opening ports
type FirewallRule struct {
	Protocol     string // "tcp", "udp", "icmp"
	Port         int    // Port number (for TCP/UDP)
	SourceNetwork string // CIDR notation, e.g., "0.0.0.0/0"
	Description  string // Human-readable description
}

// NewTCPRules creates firewall rules for opening TCP ports
func NewTCPRules(ports []int, sourceNetwork string) []FirewallRule {
	rules := make([]FirewallRule, len(ports))
	for i, port := range ports {
		rules[i] = FirewallRule{
			Protocol:      "tcp",
			Port:          port,
			SourceNetwork: sourceNetwork,
			Description:   fmt.Sprintf("TCP port %d access", port),
		}
	}
	return rules
}

// Provider defines the interface for GPU cloud providers
type Provider interface {
	// Instance management
	ListInstances() ([]*Instance, error)
	GetInstance(instanceID string) (*Instance, error)
	LaunchInstance(req *LaunchRequest) (string, error) // Returns instance ID
	TerminateInstance(instanceID string) error

	// Instance type information
	ListInstanceTypes() ([]*InstanceType, error)

	// SSH key management
	ListSSHKeys() ([]*SSHKey, error)
	CreateSSHKey(name string) (*SSHKey, error)             // Generates new key pair
	AddSSHKey(name, publicKey string) (*SSHKey, error)     // Adds existing public key

	// Firewall/Port management
	CreateFirewallRuleset(name, region string, rules []FirewallRule) (string, error)

	// Provider-specific utilities
	GetProviderName() string
}