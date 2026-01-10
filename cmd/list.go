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

// listCmd represents the list command
var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List running instances",
	Long:  `List your running instances/computers on your cloud provider.`,
	Run: func(cmd *cobra.Command, args []string) {
		provider, err := cmd.Flags().GetString("provider")
		if err != nil {
			log.Fatalf("Error getting provider: %v", err)
		}

		apiToken, err := cmd.Flags().GetString("api-token")
		if err != nil {
			log.Fatalf("Error getting API token: %v", err)
		}

		// Create HTTP client
		httpClient := remote.NewHTTPClient()

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

		// List running instances (now the default behavior)
		runningInstances, err := remote.ListRunningInstances(httpClient, apiToken)
		if err != nil {
			log.Fatalf("Error listing running instances: %v", err)
		}

		if len(runningInstances) == 0 {
			fmt.Println("No running instances found.")
			return
		}

		resourceType := "instance"
		if provider == "orgo" {
			resourceType = "computer"
		}

		fmt.Printf("Found %d running %s(s):\n\n", len(runningInstances), resourceType)
		for _, instance := range runningInstances {
			fmt.Printf("%s ID: %s\n", strings.Title(resourceType), instance.ID)
			if instance.Name != "" {
				fmt.Printf("Name: %s\n", instance.Name)
			}
			fmt.Printf("Status: %s\n", instance.Status)
			if provider != "orgo" {
				fmt.Printf("IP: %s\n", instance.IP)
			}
			fmt.Printf("Project/Region: %s", instance.Region.Name)
			if instance.Region.Description != "" {
				fmt.Printf(" (%s)", instance.Region.Description)
			}
			fmt.Println()
			fmt.Printf("Instance Type: %s", instance.InstanceType.Name)
			if instance.InstanceType.Description != "" {
				fmt.Printf(" (%s)", instance.InstanceType.Description)
			}
			fmt.Println()
			if instance.Hostname != "" && provider != "orgo" {
				fmt.Printf("Hostname: %s\n", instance.Hostname)
			}
			fmt.Println("---")
		}
	},
}

func init() {
	rootCmd.AddCommand(listCmd)

	listCmd.Flags().StringP("provider", "p", "lambda", "Cloud provider to use (lambda or orgo)")
	listCmd.Flags().StringP("api-token", "a", "", "API token for cloud provider (can also be set via LAMBDA_API_KEY or ORGO_API_KEY env var)")
}
