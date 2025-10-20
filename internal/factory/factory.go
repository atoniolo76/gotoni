package factory

import (
	"fmt"
	"toni/gpusnapshot/internal/providers"
	"toni/gpusnapshot/internal/providers/lambdalabs"
)

// NewProvider creates a provider instance based on the provider type
func NewProvider(providerType, apiToken string) (providers.Provider, error) {
	switch providerType {
	case "lambdalabs":
		return lambdalabs.NewClient(apiToken), nil
	default:
		return nil, fmt.Errorf("unsupported provider: %s", providerType)
	}
}