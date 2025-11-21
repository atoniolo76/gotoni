/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"fmt"
	"log"
	"strings"
	"time"
	"github.com/atoniolo76/gotoni/pkg/client"

	"github.com/spf13/cobra"
)

// launchCmd represents the launch command
var launchCmd = &cobra.Command{
	Use:   "launch <instance-name>",
	Short: "Launch a new instance on your Neocloud.",
	Long:  `Launch a new instance on your Neocloud with the specified name.`,
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		instanceName := args[0]
		
		instanceType, err := cmd.Flags().GetString("instance-type")
		if err != nil {
			log.Fatalf("Error getting instance type: %v", err)
		}

		region, err := cmd.Flags().GetString("region")
		if err != nil {
			log.Fatalf("Error getting region: %v", err)
		}

		apiToken, err := cmd.Flags().GetString("api-token")
		if err != nil {
			log.Fatalf("Error getting API token: %v", err)
		}

		wait, err := cmd.Flags().GetBool("wait")
		if err != nil {
			log.Fatalf("Error getting wait flag: %v", err)
		}

		waitTimeout, err := cmd.Flags().GetDuration("wait-timeout")
		if err != nil {
			log.Fatalf("Error getting wait timeout: %v", err)
		}

		filesystemName, err := cmd.Flags().GetString("filesystem")
		if err != nil {
			log.Fatalf("Error getting filesystem flag: %v", err)
		}

		// If API token not provided via flag, get from environment
		if apiToken == "" {
			apiToken = client.GetAPIToken()
			if apiToken == "" {
				log.Fatal("API token not provided via --api-token flag or LAMBDA_API_KEY environment variable")
			}
		}

		// Create HTTP client
		httpClient := client.NewHTTPClient()

		// Create filesystem if flag is provided
		if filesystemName != "" {
			fmt.Printf("Creating filesystem '%s' in region '%s'...\n", filesystemName, region)
			fs, err := client.CreateFilesystem(httpClient, apiToken, filesystemName, region)
			if err != nil {
				log.Fatalf("Error creating filesystem: %v", err)
			}
			fmt.Printf("Filesystem '%s' created successfully (ID: %s)\n", fs.Name, fs.ID)
		}

		var launchedInstances []client.LaunchedInstance
		var launchErr error

		if wait {
			// Launch and wait for instances to be ready
			launchedInstances, launchErr = client.LaunchAndWait(httpClient, apiToken, instanceType, region, 1, instanceName, "", waitTimeout, filesystemName)
			
			// If successful and we waited, update SSH config
			if launchErr == nil {
				for _, instance := range launchedInstances {
					// Get instance details to get IP
					details, err := client.GetInstance(httpClient, apiToken, instance.ID)
					if err == nil && details.IP != "" {
						if err := client.UpdateSSHConfig(instanceName, details.IP, instance.SSHKeyFile); err != nil {
							fmt.Printf("Warning: Failed to update SSH config: %v\n", err)
						}
					}
				}
			}
		} else {
			// Launch the instance (this creates SSH key and saves to config)
			launchedInstances, launchErr = client.LaunchInstance(httpClient, apiToken, instanceType, region, 1, instanceName, "", filesystemName)
			fmt.Println("Note: SSH config not updated because IP is not yet available. Use --wait flag to update SSH config automatically.")
		}

		if launchErr != nil {
			log.Fatalf("Error launching instance: %v", launchErr)
		}

		// Print instance info with SSH access details
		for _, instance := range launchedInstances {
			fmt.Printf("Launched instance: %s\n", instance.ID)
			fmt.Printf("SSH Key: %s\n", instance.SSHKeyName)
			fmt.Printf("SSH Key File: %s\n", instance.SSHKeyFile)
			
			if wait {
				fmt.Printf("\nInstance is ready!\n")
				fmt.Printf("Connect via SSH: ssh %s\n", instanceName)
				fmt.Printf("Open in VS Code: gotoni open %s\n", instanceName)
			} else {
				fmt.Printf("\nInstance is launching (SSH config not updated yet)\n")
				fmt.Printf("Connect with: ssh -i %s ubuntu@<instance-ip>\n", instance.SSHKeyFile)
				fmt.Printf("Or use: gotoni connect <instance-ip>\n")
			}
			fmt.Println()
		}
	},
}

func init() {
	rootCmd.AddCommand(launchCmd)

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// launchCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// Extract instance type keys from the map
	var instanceOptions []string
	for key := range client.MatchingInstanceTypes {
		instanceOptions = append(instanceOptions, key)
	}

	// Extract region keys from the map
	launchCmd.Flags().StringP("api-token", "a", "", "API token for cloud provider (can also be set via LAMBDA_API_KEY env var)")

	launchCmd.Flags().StringP("region", "r", "", "Region to launch the instance in (e.g., us-east-1, us-west-2)")

	launchCmd.Flags().StringP("instance-type", "t", "", `choose the instance type to launch. Options:
`+strings.Join(instanceOptions, "\n")+`
	`)

	launchCmd.Flags().BoolP("wait", "w", false, "Wait for instance to become ready before returning")

	launchCmd.Flags().DurationP("wait-timeout", "", 10*time.Minute, "Timeout for waiting for instance to become ready")

	launchCmd.Flags().StringP("filesystem", "f", "", "Create and mount a filesystem with the specified name (will be created in the same region as the instance)")

	launchCmd.MarkFlagRequired("instance-type")
	launchCmd.MarkFlagRequired("region")
}
