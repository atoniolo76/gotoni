/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
	"toni/gotoni/pkg/client"

	"github.com/spf13/cobra"
)

// listCmd represents the list command
var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all instances on your Neocloud.",
	Long:  `List all instances on your Neocloud.`,
	Run: func(cmd *cobra.Command, args []string) {
		apiToken, err := cmd.Flags().GetString("api-token")
		if err != nil {
			log.Fatalf("Error getting API token: %v", err)
		}

		// Create HTTP client
		httpClient := &http.Client{Timeout: time.Duration(30) * time.Second}

		// If API token not provided via flag, get from environment
		if apiToken == "" {
			apiToken = os.Getenv("LAMBDA_API_KEY")
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
	listCmd.Flags().StringP("api-token", "a", "", "API token for Lambda Cloud (can also be set via LAMBDA_API_KEY env var)")
	listCmd.Flags().StringP("region", "r", "", "filter by region")
}
