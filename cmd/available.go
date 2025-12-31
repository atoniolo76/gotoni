/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"fmt"
	"log"
	"sort"

	"github.com/atoniolo76/gotoni/pkg/remote"

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
		httpClient := remote.NewHTTPClient()

		// If API token not provided via flag, get from environment
		if apiToken == "" {
			apiToken = remote.GetAPIToken()
			if apiToken == "" {
				log.Fatal("API token not provided via --api-token flag or LAMBDA_API_KEY environment variable")
			}
		}

		availableInstanceTypes, err := remote.GetAvailableInstanceTypes(httpClient, apiToken)
		if err != nil {
			log.Fatalf("Error getting available instance types: %v", err)
		}

		region, err := cmd.Flags().GetString("region")
		if err != nil {
			log.Fatalf("Error getting region flag: %v", err)
		}

		sortRegion, err := cmd.Flags().GetBool("sort-region")
		if err != nil {
			log.Fatalf("Error getting sort-region flag: %v", err)
		}

		if sortRegion {
			// Group by region
			regionMap := make(map[string][]string)
			for _, instance := range availableInstanceTypes {
				for _, reg := range instance.RegionsWithCapacityAvailable {
					regionMap[reg.Name] = append(regionMap[reg.Name], instance.InstanceType.Name)
				}
			}

			// Sort regions alphabetically
			regions := make([]string, 0, len(regionMap))
			for reg := range regionMap {
				regions = append(regions, reg)
			}
			sort.Strings(regions)

			fmt.Println("Available instance types by region:")
			fmt.Println()
			for _, reg := range regions {
				instanceTypes := regionMap[reg]
				sort.Strings(instanceTypes)
				fmt.Printf("Region: %s\n", reg)
				for _, instanceType := range instanceTypes {
					fmt.Printf("  - %s\n", instanceType)
				}
				fmt.Println()
			}
		} else if region != "" {
			// Filter by region
			fmt.Printf("Available instance types in region %s:\n\n", region)
			for _, instance := range availableInstanceTypes {
				hasRegion := false
				for _, reg := range instance.RegionsWithCapacityAvailable {
					if reg.Name == region {
						hasRegion = true
						break
					}
				}
				if !hasRegion {
					continue
				}
				fmt.Printf("Instance type: %s\n", instance.InstanceType.Name)
				fmt.Println()
			}
		} else {
			// Default: show all regions for each instance type
			fmt.Println("Available instance types:")
			fmt.Println()
			for _, instance := range availableInstanceTypes {
				fmt.Printf("Instance type: %s\n", instance.InstanceType.Name)
				fmt.Printf("Regions: ")
				regionNames := make([]string, len(instance.RegionsWithCapacityAvailable))
				for i, reg := range instance.RegionsWithCapacityAvailable {
					regionNames[i] = reg.Name
				}
				sort.Strings(regionNames)
				for i, regName := range regionNames {
					if i > 0 {
						fmt.Printf(", ")
					}
					fmt.Printf("%s", regName)
				}
				fmt.Println()
				fmt.Println()
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(availableCmd)

	availableCmd.Flags().StringP("api-token", "a", "", "API token for cloud provider (can also be set via LAMBDA_API_KEY env var)")
	availableCmd.Flags().StringP("region", "r", "", "Filter available instance types by region")
	availableCmd.Flags().Bool("sort-region", false, "Group output by region, showing regions and their available instance types")
}
