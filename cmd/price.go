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

// choosing an instance is no fun. TODO: build gotoni into a tool for choosing best instannces based on workload and cost.

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

		getCurrentUsageRate, err := cmd.Flags().GetBool("current-usage")
		if err != nil {
			log.Fatalf("Error getting current-usage flag: %v", err)
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

		availableInstanceTypes, err := remote.GetAvailableInstanceTypes(httpClient, apiToken)
		if err != nil {
			log.Fatalf("Error getting available instance types: %v", err)
		}

		fmt.Println("Lambda Cloud Instance Pricing:")
		fmt.Println()

		totalCents := 0
		for _, instance := range availableInstanceTypes {
			price := instance.InstanceType.PriceCentsPerHour
			totalCents += price
			if !getCurrentUsageRate {
				fmt.Printf("%-25s %s\n", instance.InstanceType.Name, formatPrice(price))
			}
		}

		if getCurrentUsageRate {
			fmt.Printf("%-25s %s\n", "Total", formatPrice(totalCents))
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
	pricingCmd.Flags().BoolP("current-usage", "c", false, "Get current usage rate for all running instances")
}
