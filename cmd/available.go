/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"fmt"
	"log"

	"github.com/atoniolo76/gotoni/pkg/client"

	"github.com/spf13/cobra"
)

// availableCmd represents the available command
var availableCmd = &cobra.Command{
	Use:   "available",
	Short: "List available instance types",
	Long:  `List available instance types on your cloud provider.`,
	Run: func(cmd *cobra.Command, args []string) {
		apiToken, err := cmd.Flags().GetString("api-token")
		if err != nil {
			log.Fatalf("Error getting API token: %v", err)
		}

		// Create HTTP client
		httpClient := client.NewHTTPClient()

		// If API token not provided via flag, get from environment
		if apiToken == "" {
			apiToken = client.GetAPIToken()
			if apiToken == "" {
				log.Fatal("API token not provided via --api-token flag or LAMBDA_API_KEY environment variable")
			}
		}

		availableInstanceTypes, err := client.GetAvailableInstanceTypes(httpClient, apiToken)
		if err != nil {
			log.Fatalf("Error getting available instance types: %v", err)
		}

		region, err := cmd.Flags().GetString("region")
		if err != nil {
			log.Fatalf("Error getting region flag: %v", err)
		}

		if region != "" {
			fmt.Printf("Available instance types in region %s:\n\n", region)
			for _, instance := range availableInstanceTypes {
				if instance.RegionsWithCapacityAvailable[0].Name != region {
					continue
				}
				fmt.Printf("Instance type: %s\n", instance.InstanceType.Name)
				fmt.Printf("Region: %s\n", instance.RegionsWithCapacityAvailable[0].Name)
				fmt.Println()
			}
		} else {
			fmt.Println("Available instance types:")
			fmt.Println()
			for _, instance := range availableInstanceTypes {
				fmt.Printf("Instance type: %s\n", instance.InstanceType.Name)
				fmt.Printf("Region: %s\n", instance.RegionsWithCapacityAvailable[0].Name)
				fmt.Println()
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(availableCmd)

	availableCmd.Flags().StringP("api-token", "a", "", "API token for cloud provider (can also be set via LAMBDA_API_KEY env var)")
	availableCmd.Flags().StringP("region", "r", "", "Filter available instance types by region")
}
