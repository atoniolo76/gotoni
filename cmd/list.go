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

// listCmd represents the list command
var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List instances or instance types",
	Long:  `List your running instances or available instance types on your cloud provider.`,
	Run: func(cmd *cobra.Command, args []string) {
		running, err := cmd.Flags().GetBool("running")
		if err != nil {
			log.Fatalf("Error getting running flag: %v", err)
		}

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

		if running {
			// List running instances
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
		} else {
			// List available instance types (original functionality)
			availableInstanceTypes, err := client.GetAvailableInstanceTypes(httpClient, apiToken)
			if err != nil {
				log.Fatalf("Error getting available instance types: %v", err)
			}

			region, err := cmd.Flags().GetString("region")
			if err != nil {
				log.Fatalf("Error getting region flag: %v", err)
			}

			if region != "" {
				for _, instance := range availableInstanceTypes {
					if instance.RegionsWithCapacityAvailable[0].Name != region {
						continue
					}
					fmt.Printf("Instance type: %s\n", instance.InstanceType.Name)
					fmt.Printf("Region: %s\n", instance.RegionsWithCapacityAvailable[0].Name)
				}
			} else {
				for _, instance := range availableInstanceTypes {
					fmt.Printf("Instance type: %s\n", instance.InstanceType.Name)
					fmt.Printf("Region: %s\n", instance.RegionsWithCapacityAvailable[0].Name)
					fmt.Println()
				}
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(listCmd)

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// listCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	listCmd.Flags().StringP("api-token", "a", "", "API token for cloud provider (can also be set via LAMBDA_API_KEY env var)")
	listCmd.Flags().BoolP("running", "r", false, "List running instances instead of available instance types")
	listCmd.Flags().StringP("region", "", "", "Filter available instance types by region (only used when not listing running instances)")
}
