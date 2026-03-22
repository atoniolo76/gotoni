/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/atoniolo76/gotoni/pkg/remote"
	"github.com/atoniolo76/gotoni/pkg/spicedb"

	"github.com/spf13/cobra"
)

func resourceTypeForProvider(provider string) string {
	switch provider {
	case "orgo":
		return "computer"
	case "modal":
		return "sandbox"
	default:
		return "instance"
	}
}

func newCloudProvider(provider string) remote.CloudProvider {
	switch provider {
	case "modal":
		return remote.NewModalProvider()
	case "orgo":
		return remote.NewOrgoProvider()
	default:
		return remote.NewLambdaProvider()
	}
}

func providerType(provider string) remote.CloudProviderType {
	switch provider {
	case "modal":
		return remote.CloudProviderModal
	case "orgo":
		return remote.CloudProviderOrgo
	default:
		return remote.CloudProviderLambda
	}
}

func resolveAPIToken(provider string, flagToken string) string {
	if flagToken != "" {
		return flagToken
	}
	token := remote.GetAPITokenForProvider(providerType(provider))
	if token == "" {
		envVar := map[string]string{
			"lambda": "LAMBDA_API_KEY",
			"orgo":   "ORGO_API_KEY",
			"modal":  "MODAL_TOKEN_ID",
		}
		name := envVar[provider]
		if name == "" {
			name = "LAMBDA_API_KEY"
		}
		log.Fatalf("API token not provided via --api-token flag or %s environment variable", name)
	}
	return token
}

var killCmd = &cobra.Command{
	Use:   "kill [names...]",
	Short: "Terminate instances/sandboxes/computers",
	Long: `Terminate one or more resources by name or ID.

Works with all providers:
  - Lambda Cloud instances
  - Modal sandboxes
  - Orgo computers

Use --all to terminate every running resource for the selected provider.`,
	Run: func(cmd *cobra.Command, args []string) {
		provider, _ := cmd.Flags().GetString("provider")
		flagToken, _ := cmd.Flags().GetString("api-token")
		killAll, _ := cmd.Flags().GetBool("all")

		apiToken := resolveAPIToken(provider, flagToken)
		httpClient := remote.NewHTTPClient()
		cloudProvider := newCloudProvider(provider)
		resType := resourceTypeForProvider(provider)

		var targetNames []string

		if killAll {
			instances, err := cloudProvider.ListRunningInstances(httpClient, apiToken)
			if err != nil {
				log.Fatalf("Error listing running %ss: %v", resType, err)
			}
			if len(instances) == 0 {
				fmt.Printf("No running %ss to terminate.\n", resType)
				return
			}
			for _, inst := range instances {
				targetNames = append(targetNames, inst.Name)
				if inst.Name == "" {
					targetNames[len(targetNames)-1] = inst.ID
				}
			}
		} else {
			if len(args) > 0 {
				targetNames = args
			} else {
				namesFlag, _ := cmd.Flags().GetStringSlice("instance-names")
				targetNames = namesFlag
			}
		}

		if len(targetNames) == 0 {
			log.Fatalf("No %s names provided. Use 'gotoni kill <name>' or 'gotoni kill --all'", resType)
		}

		// Resolve names/IDs to actual instance IDs
		var instanceIDs []string
		var resolvedNames []string
		for _, nameOrID := range targetNames {
			id, displayName, err := resolveInstanceID(cloudProvider, httpClient, apiToken, nameOrID)
			if err != nil {
				log.Fatalf("Failed to resolve %s %q: %v", resType, nameOrID, err)
			}
			instanceIDs = append(instanceIDs, id)
			resolvedNames = append(resolvedNames, displayName)
		}

		ctx := context.Background()
		for _, id := range instanceIDs {
			if err := spicedb.Check(ctx, "resource", id, "delete"); err != nil {
				log.Fatalf("Permission denied for %s: %v", id, err)
			}
		}

		fmt.Printf("Terminating %s(s): %s\n", resType, strings.Join(resolvedNames, ", "))

		resp, err := cloudProvider.TerminateInstance(httpClient, apiToken, instanceIDs)
		if err != nil {
			log.Fatalf("Error terminating %s(s): %v", resType, err)
		}

		for _, id := range instanceIDs {
			spicedb.DeleteResource(ctx, id)
		}

		// Lambda manages state via API, but we still clean up any leftover config refs
		if provider == "lambda" {
			for _, id := range instanceIDs {
				if rmErr := remote.RemoveInstanceFromConfig(id); rmErr != nil {
					log.Printf("Warning: failed to remove %s %s from config: %v", resType, id, rmErr)
				}
			}
		}

		fmt.Printf("Successfully terminated %d %s(s):\n", len(resp.TerminatedInstances), resType)
		for _, inst := range resp.TerminatedInstances {
			fmt.Printf("  - %s: %s\n", inst.ID, inst.Status)
		}
	},
}

// resolveInstanceID resolves a name or ID to a concrete instance ID.
// Returns (id, displayName, error).
func resolveInstanceID(cp remote.CloudProvider, httpClient *http.Client, apiToken string, nameOrID string) (string, string, error) {
	// First, try GetInstance directly (works for IDs and, on Modal, names via DB)
	if inst, err := cp.GetInstance(httpClient, apiToken, nameOrID); err == nil {
		return inst.ID, inst.Name, nil
	}

	// Fall back to listing and matching by name or ID
	instances, err := cp.ListRunningInstances(httpClient, apiToken)
	if err != nil {
		return "", "", fmt.Errorf("failed to list running instances: %w", err)
	}

	for _, inst := range instances {
		if inst.Name == nameOrID || inst.ID == nameOrID {
			return inst.ID, inst.Name, nil
		}
	}

	return "", "", fmt.Errorf("%q not found among running resources", nameOrID)
}

func init() {
	rootCmd.AddCommand(killCmd)

	killCmd.Flags().StringP("provider", "p", "lambda", "Cloud provider (lambda, orgo, or modal)")
	killCmd.Flags().StringP("api-token", "a", "", "API token (or set LAMBDA_API_KEY / ORGO_API_KEY / MODAL_TOKEN_ID)")
	killCmd.Flags().StringSliceP("instance-names", "i", []string{}, "Names to terminate (can also be provided as positional arguments)")
	killCmd.Flags().Bool("all", false, "Terminate all running resources for the selected provider")
}
