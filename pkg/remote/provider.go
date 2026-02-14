package remote

import (
	"net/http"
	"os"
	"time"
)

// CloudProvider defines the interface that all cloud providers must implement
type CloudProvider interface {
	// Instance Management
	LaunchInstance(httpClient *http.Client, apiToken string, instanceType string, region string, quantity int, name string, sshKeyName string, filesystemName string) ([]LaunchedInstance, error)
	LaunchAndWait(httpClient *http.Client, apiToken string, instanceType string, region string, quantity int, name string, sshKeyName string, timeout time.Duration, filesystemName string) ([]LaunchedInstance, error)
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

	// Remote Command Execution (for providers that support it)
	ExecuteBashCommand(instanceID string, command string) (string, error)
	ExecutePythonCode(instanceID string, code string, timeout int) (string, error)
}

// FileUploader is an optional interface for providers that support direct file uploads
// Modal implements this using the sandbox filesystem API
// Lambda/Orgo can use SCP via SSH (implemented separately)
type FileUploader interface {
	UploadFile(instanceID string, remotePath string, content []byte) error
	UploadFileFromPath(instanceID string, localPath string, remotePath string) error
	DownloadFile(instanceID string, remotePath string) ([]byte, error)
}

// CloudProviderType represents the type of cloud provider
type CloudProviderType string

const (
	CloudProviderLambda CloudProviderType = "lambda"
	CloudProviderOrgo   CloudProviderType = "orgo"
	CloudProviderModal  CloudProviderType = "modal"
)

// GetCloudProvider returns the appropriate cloud provider based on the GOTONI_CLOUD environment variable
func GetCloudProvider() (CloudProvider, CloudProviderType) {
	cloudType := CloudProviderType(getCloudProviderType())
	switch cloudType {
	case CloudProviderLambda:
		return NewLambdaProvider(), CloudProviderLambda
	case CloudProviderOrgo:
		return NewOrgoProvider(), CloudProviderOrgo
	case CloudProviderModal:
		return NewModalProvider(), CloudProviderModal
	default:
		return NewLambdaProvider(), CloudProviderLambda
	}
}

// getCloudProviderType returns the cloud provider type from environment variable
// If GOTONI_CLOUD is not set, automatically detects based on which API keys are exported
func getCloudProviderType() string {
	// Check if explicitly set
	cloud := os.Getenv("GOTONI_CLOUD")
	if cloud != "" {
		return cloud
	}

	// Auto-detect based on which credentials are exported
	hasLambda := os.Getenv("LAMBDA_API_KEY") != ""
	hasModal := os.Getenv("MODAL_TOKEN_ID") != ""
	hasOrgo := os.Getenv("ORGO_API_KEY") != ""

	// Count how many are set
	count := 0
	if hasLambda {
		count++
	}
	if hasModal {
		count++
	}
	if hasOrgo {
		count++
	}

	// If only one is set, use that
	if count == 1 {
		if hasLambda {
			return string(CloudProviderLambda)
		}
		if hasModal {
			return string(CloudProviderModal)
		}
		if hasOrgo {
			return string(CloudProviderOrgo)
		}
	}

	// If multiple or none are set, default to Lambda for backward compatibility
	// User can explicitly set GOTONI_CLOUD to override
	return string(CloudProviderLambda)
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
	case CloudProviderOrgo:
		return os.Getenv("ORGO_API_KEY")
	case CloudProviderModal:
		return os.Getenv("MODAL_TOKEN_ID")
	default:
		return os.Getenv("LAMBDA_API_KEY")
	}
}
