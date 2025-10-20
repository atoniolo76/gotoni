package providers

import "fmt"

// NewProvider creates a provider instance based on the provider type
func NewProvider(providerType, apiToken string) (Provider, error) {
	switch providerType {
	case "lambdalabs":
		// TODO: Implement Lambda Labs client
		return nil, fmt.Errorf("Lambda Labs client not yet implemented")
	case "runpod":
		// TODO: Implement RunPod client
		return nil, fmt.Errorf("RunPod client not yet implemented")
	default:
		return nil, fmt.Errorf("unsupported provider: %s", providerType)
	}
}

/*
// When implementations are complete, it should be:
func NewProvider(providerType, apiToken string) (Provider, error) {
	switch providerType {
	case "lambdalabs":
		return NewLambdaLabsClient(apiToken), nil  // Success case
	case "runpod":  
		return NewRunPodClient(apiToken), nil     // Success case
	default:
		return nil, fmt.Errorf("unsupported provider: %s", providerType)
	}
}
	*/