/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"fmt"
	"log"
	"strings"

	"github.com/atoniolo76/gotoni/pkg/remote"

	"github.com/spf13/cobra"
)

// deleteCmd represents the delete command
var deleteCmd = &cobra.Command{
	Use:   "delete [instance-names...]",
	Short: "Terminate instances/computers",
	Long:  `Terminate one or more instances on Lambda Cloud or computers on Orgo by providing their names.`,
	Run: func(cmd *cobra.Command, args []string) {
		provider, err := cmd.Flags().GetString("provider")
		if err != nil {
			log.Fatalf("Error getting provider: %v", err)
		}

		apiToken, err := cmd.Flags().GetString("api-token")
		if err != nil {
			log.Fatalf("Error getting API token: %v", err)
		}

		// If API token not provided via flag, get from environment based on provider
		if apiToken == "" {
			if provider == "orgo" {
				apiToken = remote.GetAPITokenForProvider(remote.CloudProviderOrgo)
			} else {
				apiToken = remote.GetAPITokenForProvider(remote.CloudProviderLambda)
			}
			if apiToken == "" {
				if provider == "orgo" {
					log.Fatal("API token not provided via --api-token flag or ORGO_API_KEY environment variable")
				} else {
					log.Fatal("API token not provided via --api-token flag or LAMBDA_API_KEY environment variable")
				}
			}
		}

		// Get instance names from args or flags
		var instanceNames []string

		if len(args) > 0 {
			// Instance names provided as arguments
			instanceNames = args
		} else {
			// Check for instance names flag
			namesFlag, err := cmd.Flags().GetStringSlice("instance-names")
			if err != nil {
				log.Fatalf("Error getting instance names: %v", err)
			}
			instanceNames = namesFlag
		}

		if len(instanceNames) == 0 {
			log.Fatal("No instance names provided. Use 'gotoni delete <instance-name>' or 'gotoni delete --instance-names <name1,name2>'")
		}

		// Create HTTP client
		httpClient := remote.NewHTTPClient()

		// Resolve instance names to IDs
		var instanceIDs []string
		for _, name := range instanceNames {
			instance, err := remote.ResolveInstance(httpClient, apiToken, name)
			if err != nil {
				log.Fatalf("Failed to resolve instance '%s': %v", name, err)
			}
			instanceIDs = append(instanceIDs, instance.ID)
		}

		resourceType := "instance"
		if provider == "orgo" {
			resourceType = "computer"
		}

		fmt.Printf("Terminating %s(s): %s\n", resourceType, strings.Join(instanceNames, ", "))

		terminatedResponse, err := remote.TerminateInstance(httpClient, apiToken, instanceIDs)
		if err != nil {
			log.Fatalf("Error terminating %s: %v", resourceType, err)
		}

		// Remove terminated instances from config (only for Lambda)
		if provider != "orgo" {
			// Remove all requested instance IDs, not just the ones in the response
			// (in case some were already terminated or not returned by API)
			for _, instanceID := range instanceIDs {
				if err := remote.RemoveInstanceFromConfig(instanceID); err != nil {
					log.Printf("Warning: failed to remove %s %s from config: %v", resourceType, instanceID, err)
				} else {
					fmt.Printf("Removed %s %s from config\n", resourceType, instanceID)
				}
			}
		}

		fmt.Printf("Successfully terminated %d %s(s):\n", len(terminatedResponse.TerminatedInstances), resourceType)
		for _, instance := range terminatedResponse.TerminatedInstances {
			fmt.Printf("  - %s %s: %s\n", strings.Title(resourceType), instance.ID, instance.Status)
		}
	},
}

func init() {
	rootCmd.AddCommand(deleteCmd)

	// Here you will define your flags and configuration settings.
	deleteCmd.Flags().StringP("provider", "p", "lambda", "Cloud provider to use (lambda or orgo)")
	deleteCmd.Flags().StringP("api-token", "a", "", "API token for cloud provider (can also be set via LAMBDA_API_KEY or ORGO_API_KEY env var)")
	deleteCmd.Flags().StringSliceP("instance-names", "i", []string{}, "Instance/computer names to terminate (can also be provided as arguments)")
}
