package providers

import "fmt"

// NewProvider creates a provider instance based on the provider type
// This is a placeholder - in a real implementation, you'd use dependency injection
// or a registry pattern to avoid import cycles
func NewProvider(providerType, apiToken string) (Provider, error) {
	switch providerType {
	case "lambdalabs":
		// Import cycle issue - would need to restructure
		// For now, return an error
		return nil, fmt.Errorf("Lambda Labs client needs to be instantiated directly to avoid import cycles")
	case "runpod":
		// TODO: Implement RunPod client
		return nil, fmt.Errorf("RunPod client not yet implemented")
	default:
		return nil, fmt.Errorf("unsupported provider: %s", providerType)
	}
}