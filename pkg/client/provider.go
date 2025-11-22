package client

import (
	"net/http"
	"os"
	"time"
)

// CloudProvider defines the interface that all cloud providers must implement
type CloudProvider interface {
	// Instance Management
	LaunchInstance(httpClient *http.Client, apiToken string, instanceType string, region string, quantity int, name string, sshKeyName string, filesystemName string, dockerImage string) ([]LaunchedInstance, error)
	LaunchAndWait(httpClient *http.Client, apiToken string, instanceType string, region string, quantity int, name string, sshKeyName string, timeout time.Duration, filesystemName string, dockerImage string) ([]LaunchedInstance, error)
	GetInstance(httpClient *http.Client, apiToken string, instanceID string) (*RunningInstance, error)
	ListRunningInstances(httpClient *http.Client, apiToken string) ([]RunningInstance, error)
	TerminateInstance(httpClient *http.Client, apiToken string, instanceIDs []string) (*InstanceTerminateResponse, error)
	WaitForInstanceReady(httpClient *http.Client, apiToken string, instanceID string, timeout time.Duration) error

	// SSH Key Management
	CreateSSHKeyForProject(httpClient *http.Client, apiToken string) (string, string, error)
	ListSSHKeys(httpClient *http.Client, apiToken string) ([]SSHKey, error)
	DeleteSSHKey(httpClient *http.Client, apiToken string, sshKeyID string) error

	// Instance Types
	GetAvailableInstanceTypes(httpClient *http.Client, apiToken string) ([]Instance, error)
	CheckInstanceTypeAvailability(httpClient *http.Client, apiToken string, instanceTypeName string) ([]Region, error)

	// Filesystem Management
	CreateFilesystem(httpClient *http.Client, apiToken string, name string, region string) (*Filesystem, error)
	GetFilesystemInfo(filesystemName string) (*FilesystemInfo, error)
	ListFilesystems(httpClient *http.Client, apiToken string) ([]Filesystem, error)
	DeleteFilesystem(httpClient *http.Client, apiToken string, filesystemID string) error

	// Firewall Management
	GetGlobalFirewallRules(httpClient *http.Client, apiToken string) (*GlobalFirewallRuleset, error)
	UpdateGlobalFirewallRules(httpClient *http.Client, apiToken string, rules []FirewallRule) (*GlobalFirewallRuleset, error)
	EnsurePortOpen(httpClient *http.Client, apiToken string, port int, protocol string, description string) error
}

// CloudProviderType represents the type of cloud provider
type CloudProviderType string

const (
	CloudProviderLambda CloudProviderType = "lambda"
)

// GetCloudProvider returns the appropriate cloud provider based on the GOTONI_CLOUD environment variable
func GetCloudProvider() (CloudProvider, CloudProviderType) {
	cloudType := CloudProviderType(getCloudProviderType())
	switch cloudType {
	case CloudProviderLambda:
		return NewLambdaProvider(), CloudProviderLambda
	default:
		return NewLambdaProvider(), CloudProviderLambda
	}
}

// getCloudProviderType returns the cloud provider type from environment variable
func getCloudProviderType() string {
	cloud := os.Getenv("GOTONI_CLOUD")
	if cloud == "" {
		return string(CloudProviderLambda) // Default to lambda
	}
	return cloud
}

// GetAPIToken returns the API token from environment variable based on the current cloud provider
func GetAPIToken() string {
	_, providerType := GetCloudProvider()
	return GetAPITokenForProvider(providerType)
}

// GetAPITokenForProvider returns the API token for a specific provider
func GetAPITokenForProvider(providerType CloudProviderType) string {
	switch providerType {
	case CloudProviderLambda:
		return os.Getenv("LAMBDA_API_KEY")
	default:
		return os.Getenv("LAMBDA_API_KEY")
	}
}
