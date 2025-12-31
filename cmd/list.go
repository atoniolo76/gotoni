/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"fmt"
	"log"

	"github.com/atoniolo76/gotoni/pkg/remote"

	"github.com/spf13/cobra"
)

// listCmd represents the list command
var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List running instances",
	Long:  `List your running instances on your cloud provider.`,
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

		// List running instances (now the default behavior)
		runningInstances, err := client.ListRunningInstances(httpClient, apiToken)
		if err != nil {
			log.Fatalf("Error listing running instances: %v", err)
		}

		if len(runningInstances) == 0 {
			fmt.Println("No running instances found.")
			return
		}

		fmt.Printf("Found %d running instance(s):\n\n", len(runningInstances))
		for _, instance := range runningInstances {
			fmt.Printf("Instance ID: %s\n", instance.ID)
			if instance.Name != "" {
				fmt.Printf("Name: %s\n", instance.Name)
			}
			fmt.Printf("Status: %s\n", instance.Status)
			fmt.Printf("IP: %s\n", instance.IP)
			fmt.Printf("Region: %s (%s)\n", instance.Region.Name, instance.Region.Description)
			fmt.Printf("Instance Type: %s (%s)\n", instance.InstanceType.Name, instance.InstanceType.Description)
			if instance.Hostname != "" {
				fmt.Printf("Hostname: %s\n", instance.Hostname)
			}
			fmt.Println("---")
		}
	},
}

func init() {
	rootCmd.AddCommand(listCmd)

	listCmd.Flags().StringP("api-token", "a", "", "API token for cloud provider (can also be set via LAMBDA_API_KEY env var)")
}
