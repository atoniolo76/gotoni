/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/atoniolo76/gotoni/pkg/client"

	"github.com/spf13/cobra"
)

// deleteCmd represents the delete command
var deleteCmd = &cobra.Command{
	Use:   "delete [instance-names...]",
	Short: "Terminate instances on Lambda Cloud",
	Long:  `Terminate one or more instances on Lambda Cloud by providing their instance names.`,
	Run: func(cmd *cobra.Command, args []string) {
		apiToken, err := cmd.Flags().GetString("api-token")
		if err != nil {
			log.Fatalf("Error getting API token: %v", err)
		}

		// If API token not provided via flag, get from environment
		if apiToken == "" {
			apiToken = os.Getenv("LAMBDA_API_KEY")
			if apiToken == "" {
				log.Fatal("API token not provided via --api-token flag or LAMBDA_API_KEY environment variable")
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
		httpClient := client.NewHTTPClient()

		// Resolve instance names to IDs
		var instanceIDs []string
		for _, name := range instanceNames {
			instance, err := client.ResolveInstance(httpClient, apiToken, name)
			if err != nil {
				log.Fatalf("Failed to resolve instance '%s': %v", name, err)
			}
			instanceIDs = append(instanceIDs, instance.ID)
		}

		fmt.Printf("Terminating instance(s): %s\n", strings.Join(instanceNames, ", "))

		terminatedResponse, err := client.TerminateInstance(httpClient, apiToken, instanceIDs)
		if err != nil {
			log.Fatalf("Error terminating instance: %v", err)
		}

		// Remove terminated instances from config
		// Remove all requested instance IDs, not just the ones in the response
		// (in case some were already terminated or not returned by API)
		for _, instanceID := range instanceIDs {
			if err := client.RemoveInstanceFromConfig(instanceID); err != nil {
				log.Printf("Warning: failed to remove instance %s from config: %v", instanceID, err)
			} else {
				fmt.Printf("Removed instance %s from config\n", instanceID)
			}
		}

		fmt.Printf("Successfully terminated %d instance(s):\n", len(terminatedResponse.TerminatedInstances))
		for _, instance := range terminatedResponse.TerminatedInstances {
			fmt.Printf("  - Instance %s: %s\n", instance.ID, instance.Status)
		}
	},
}

func init() {
	rootCmd.AddCommand(deleteCmd)

	// Here you will define your flags and configuration settings.
	deleteCmd.Flags().StringP("api-token", "a", "", "API token for cloud provider (can also be set via LAMBDA_API_KEY env var)")
	deleteCmd.Flags().StringSliceP("instance-names", "i", []string{}, "Instance names to terminate (can also be provided as arguments)")
}
