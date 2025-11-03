/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"toni/gotoni/pkg/client"

	"github.com/spf13/cobra"
)

// deleteCmd represents the delete command
var deleteCmd = &cobra.Command{
	Use:   "delete [instance-ids...]",
	Short: "Terminate instances on Lambda Cloud",
	Long:  `Terminate one or more instances on Lambda Cloud by providing their instance IDs.`,
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

		// Get instance IDs from args or flags
		var instanceIDs []string

		if len(args) > 0 {
			// Instance IDs provided as arguments
			instanceIDs = args
		} else {
			// Check for instance IDs flag
			idsFlag, err := cmd.Flags().GetStringSlice("instance-ids")
			if err != nil {
				log.Fatalf("Error getting instance IDs: %v", err)
			}
			instanceIDs = idsFlag
		}

		if len(instanceIDs) == 0 {
			log.Fatal("No instance IDs provided. Use 'gotoni delete <instance-id>' or 'gotoni delete --instance-ids <id1,id2>'")
		}

		// Create HTTP client
		httpClient := &http.Client{Timeout: time.Duration(30) * time.Second}

		fmt.Printf("Terminating instance(s): %s\n", strings.Join(instanceIDs, ", "))

		terminatedResponse, err := client.TerminateInstance(httpClient, apiToken, instanceIDs)
		if err != nil {
			log.Fatalf("Error terminating instance: %v", err)
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
	deleteCmd.Flags().StringP("api-token", "a", "", "API token for Lambda Cloud (can also be set via LAMBDA_API_KEY env var)")
	deleteCmd.Flags().StringSliceP("instance-ids", "i", []string{}, "Instance IDs to terminate (can also be provided as arguments)")
}
