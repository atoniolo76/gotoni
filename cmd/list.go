/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"fmt"
	"log"
	"os"
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
			} else if provider == "modal" {
				apiToken = os.Getenv("MODAL_TOKEN_ID")
			} else {
				apiToken = remote.GetAPITokenForProvider(remote.CloudProviderLambda)
			}
			if apiToken == "" {
				if provider == "orgo" {
					log.Fatal("API token not provided via --api-token flag or ORGO_API_KEY environment variable")
				} else if provider == "modal" {
					log.Fatal("API token not provided via --api-token flag or MODAL_TOKEN_ID environment variable")
				} else {
					log.Fatal("API token not provided via --api-token flag or LAMBDA_API_KEY environment variable")
				}
			}
		}

		// Create provider based on flag
		var cloudProvider remote.CloudProvider
		switch provider {
		case "modal":
			cloudProvider = remote.NewModalProvider()
		case "orgo":
			cloudProvider = remote.NewOrgoProvider()
		default:
			cloudProvider = remote.NewLambdaProvider()
		}

		// List running instances
		runningInstances, err := cloudProvider.ListRunningInstances(httpClient, apiToken)
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
		} else if provider == "modal" {
			resourceType = "sandbox"
		}

		fmt.Printf("Found %d running %s(s):\n\n", len(runningInstances), resourceType)
		for _, instance := range runningInstances {
			fmt.Printf("%s ID: %s\n", strings.Title(resourceType), instance.ID)
			if instance.Name != "" {
				fmt.Printf("Name: %s\n", instance.Name)
			}
			fmt.Printf("Status: %s\n", instance.Status)
			if provider != "orgo" && provider != "modal" {
				fmt.Printf("IP: %s\n", instance.IP)
			}
			if provider != "modal" {
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
			}
			if instance.Hostname != "" && provider != "orgo" && provider != "modal" {
				fmt.Printf("Hostname: %s\n", instance.Hostname)
			}
			fmt.Println("---")
		}
	},
}

func init() {
	rootCmd.AddCommand(listCmd)

	listCmd.Flags().StringP("provider", "p", "lambda", "Cloud provider to use (lambda, orgo, or modal)")
	listCmd.Flags().StringP("api-token", "a", "", "API token for cloud provider (can also be set via LAMBDA_API_KEY, ORGO_API_KEY, or MODAL_TOKEN_ID env vars)")
}
