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

// choosing an instance is no fun. TODO: build gotoni into a tool for choosing best instances based on workload and cost.

// pricingCmd represents the pricing command
var pricingCmd = &cobra.Command{
	Use:   "price",
	Short: "Show pricing for instance types",
	Long:  `Display pricing information for available Lambda Cloud instance types.`,
	Run: func(cmd *cobra.Command, args []string) {
		apiToken, err := cmd.Flags().GetString("api-token")
		if err != nil {
			log.Fatalf("Error getting API token: %v", err)
		}

		showRunningInstances, err := cmd.Flags().GetBool("running")
		if err != nil {
			log.Fatalf("Error getting running flag: %v", err)
		}

		// Create HTTP client
		httpClient := remote.NewHTTPClient()

		// If API token not provided via flag, get from environment
		if apiToken == "" {
			apiToken = remote.GetAPIToken()
			if apiToken == "" {
				log.Fatal("API token not provided via --api-token flag or LAMBDA_API_KEY/LAMBDA_API_KEY environment variable")
			}
		}

		if showRunningInstances {
			// Show current running instances and their costs
			runningInstances, err := remote.ListRunningInstances(httpClient, apiToken)
			if err != nil {
				log.Fatalf("Error getting running instances: %v", err)
			}

			fmt.Println("Current Running Instances:")
			fmt.Println()

			if len(runningInstances) == 0 {
				fmt.Println("No running instances found.")
				return
			}

			totalCents := 0
			for _, instance := range runningInstances {
				price := instance.InstanceType.PriceCentsPerHour
				totalCents += price
				fmt.Printf("%-25s %-15s %s/hour\n", instance.Name, instance.InstanceType.Name, formatPrice(price))
			}
			fmt.Println()
			fmt.Printf("%-41s %s/hour\n", "Total Current Cost", formatPrice(totalCents))
		} else {
			// Show available instance types pricing
			availableInstanceTypes, err := remote.GetAvailableInstanceTypes(httpClient, apiToken)
			if err != nil {
				log.Fatalf("Error getting available instance types: %v", err)
			}

			fmt.Println("Lambda Cloud Instance Pricing:")
			fmt.Println()

			for _, instance := range availableInstanceTypes {
				price := instance.InstanceType.PriceCentsPerHour
				fmt.Printf("%-25s %s/hour\n", instance.InstanceType.Name, formatPrice(price))
			}
		}

	},
}

func formatPrice(cents int) string {
	dollars := cents / 100
	remainingCents := cents % 100
	return fmt.Sprintf("$%d.%02d", dollars, remainingCents)
}

func init() {
	rootCmd.AddCommand(pricingCmd)

	pricingCmd.Flags().StringP("api-token", "a", "", "API token for cloud provider (can also be set via LAMBDA_API_KEY env var)")
	pricingCmd.Flags().BoolP("running", "r", false, "Show current running instances and their costs instead of available instance types")
}
