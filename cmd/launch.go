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
	Short: "Launch a new instance on your cloud provider.",
	Long:  `Launch a new instance on your cloud provider with the specified name. Use --wait to automatically wait for the instance type to become available.`,
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

		pollInterval, err := cmd.Flags().GetDuration("poll-interval")
		if err != nil {
			log.Fatalf("Error getting poll interval: %v", err)
		}

		pollTimeout, err := cmd.Flags().GetDuration("poll-timeout")
		if err != nil {
			log.Fatalf("Error getting poll timeout: %v", err)
		}

		waitTimeout, err := cmd.Flags().GetDuration("wait-timeout")
		if err != nil {
			log.Fatalf("Error getting wait timeout: %v", err)
		}

		filesystemName, err := cmd.Flags().GetString("filesystem")
		if err != nil {
			log.Fatalf("Error getting filesystem flag: %v", err)
		}

		// Validate region requirement
		if region == "" && !wait {
			log.Fatal("Region is required when not using --wait flag. Use --region to specify a region or --wait to auto-select.")
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

		var launchedInstances []client.LaunchedInstance
		var launchErr error
		var actualRegion = region

		if wait {
			// Wait for instance type to become available, then launch
			fmt.Printf("Waiting for instance type %s to become available...\n", instanceType)
			fmt.Printf("Polling every %v\n", pollInterval)
			if pollTimeout > 0 {
				fmt.Printf("Timeout: %v\n", pollTimeout)
			} else {
				fmt.Printf("No timeout (will poll indefinitely)\n")
			}
			fmt.Println()

			// Create ticker for polling
			ticker := time.NewTicker(pollInterval)
			defer ticker.Stop()

			// Create timeout channel if timeout is set
			var timeoutChan <-chan time.Time
			if pollTimeout > 0 {
				timeoutChan = time.After(pollTimeout)
			}

			// Start polling
			for {
				select {
				case <-timeoutChan:
					log.Fatalf("Timeout reached after %v. Instance type %s did not become available.", pollTimeout, instanceType)

				case <-ticker.C:
					fmt.Printf("[%s] Checking availability for %s...\n", time.Now().Format("15:04:05"), instanceType)

					regions, err := client.CheckInstanceTypeAvailability(httpClient, apiToken, instanceType)
					if err != nil {
						fmt.Printf("Error checking availability: %v\n", err)
						continue
					}

					if len(regions) > 0 {
						// Instance type is available!
						fmt.Printf("\n✓ Instance type %s is now available in %d region(s)!\n", instanceType, len(regions))
						for _, r := range regions {
							fmt.Printf("  - %s (%s)\n", r.Name, r.Description)
						}

						// Use specified region if available, otherwise use first available
						if region != "" {
							// Check if specified region is available
							found := false
							for _, r := range regions {
								if r.Name == region {
									found = true
									break
								}
							}
							if !found {
								fmt.Printf("Specified region %s not available, using first available region\n", region)
								actualRegion = regions[0].Name
							} else {
								actualRegion = region
							}
						} else {
							actualRegion = regions[0].Name
						}
						fmt.Printf("Using region: %s\n", actualRegion)
						goto launchInstance
					} else {
						fmt.Printf("  Not available yet. Retrying in %v...\n", pollInterval)
					}
				}
			}
		}

	launchInstance:
		// Create or retrieve filesystem if flag is provided
		if filesystemName != "" {
			// Check if filesystem already exists
			existingFilesystems, err := client.ListFilesystems(httpClient, apiToken)
			if err != nil {
				log.Fatalf("Error listing filesystems: %v", err)
			}

			var foundFS *client.Filesystem
			for i := range existingFilesystems {
				if existingFilesystems[i].Name == filesystemName {
					foundFS = &existingFilesystems[i]
					break
				}
			}

			if foundFS != nil {
				fmt.Printf("Found existing filesystem '%s' (ID: %s)\n", foundFS.Name, foundFS.ID)

				// Verify region
				if foundFS.Region.Name != actualRegion {
					log.Fatalf("Filesystem '%s' is in region '%s', but instance is being launched in '%s'. Regions must match.",
						filesystemName, foundFS.Region.Name, actualRegion)
				}

				// Update local config to ensure LaunchInstance can find it
				if err := client.SaveFilesystemInfo(filesystemName, foundFS.ID, foundFS.Region.Name); err != nil {
					log.Fatalf("Error saving filesystem info: %v", err)
				}

			} else {
				fmt.Printf("Creating filesystem '%s' in region '%s'...\n", filesystemName, actualRegion)
				fs, err := client.CreateFilesystem(httpClient, apiToken, filesystemName, actualRegion)
				if err != nil {
					log.Fatalf("Error creating filesystem: %v", err)
				}
				fmt.Printf("Filesystem '%s' created successfully (ID: %s)\n", fs.Name, fs.ID)
			}
		}

		fmt.Printf("\nLaunching instance...\n")
		if wait {
			// Launch and wait for instances to be ready
			launchedInstances, launchErr = client.LaunchAndWait(httpClient, apiToken, instanceType, actualRegion, 1, instanceName, "", waitTimeout, filesystemName)
		} else {
			// Launch the instance (this creates SSH key and saves to config)
			launchedInstances, launchErr = client.LaunchInstance(httpClient, apiToken, instanceType, actualRegion, 1, instanceName, "", filesystemName)
			fmt.Println("Note: SSH config not updated because IP is not yet available. Use --wait flag to update SSH config automatically.")
		}

		// If successful and we waited, update SSH config
		if launchErr == nil && wait {
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

	launchCmd.Flags().BoolP("wait", "w", false, "Wait for instance type to become available before launching, then wait for instance to be ready")

	launchCmd.Flags().DurationP("poll-interval", "", 5*time.Second, "Polling interval when waiting for instance availability (only used with --wait)")

	launchCmd.Flags().DurationP("poll-timeout", "", 0, "Maximum time to poll for instance availability (0 = no timeout, only used with --wait)")

	launchCmd.Flags().DurationP("wait-timeout", "", 10*time.Minute, "Timeout for waiting for instance to become ready after launching")

	launchCmd.Flags().StringP("filesystem", "f", "", "Create and mount a filesystem with the specified name (will be created in the same region as the instance)")

	launchCmd.MarkFlagRequired("instance-type")
	// Region is required unless --wait is used (which auto-selects region)
	// We'll validate this in the Run function
}
