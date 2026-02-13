/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/atoniolo76/gotoni/pkg/db"
	"github.com/atoniolo76/gotoni/pkg/remote"

	"github.com/spf13/cobra"
)

// launchCmd represents the launch command
var launchCmd = &cobra.Command{
	Use:   "launch [instance-name]",
	Short: "Launch a new instance and optionally run automation tasks",
	Long: `Launch a new instance on your cloud provider with the specified name.
Use --wait to automatically wait for the instance type to become available.
Use --tasks to specify automation tasks to run after the instance is ready (Lambda only).
Use --provider to specify the cloud provider (lambda or orgo).

Examples:
  # Launch Lambda GPU instance
  gotoni launch my-instance -t gpu_1x_a100 -r us-west-1 --wait

  # Launch Orgo computer
  gotoni launch my-computer --provider orgo -t 4gb -r my-project --wait

  # Launch with tasks (Lambda only)
  gotoni launch my-instance -t gpu_1x_a100 -r us-west-1 --wait --tasks "Install Docker,Setup Environment"`,
	Args: cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		var instanceName string
		if len(args) > 0 {
			instanceName = args[0]
		} else {
			instanceName = fmt.Sprintf("gotoni-%d", time.Now().Unix())
			fmt.Printf("No instance name provided. Auto-generated name: %s\n", instanceName)
		}

		instanceType, err := cmd.Flags().GetString("instance-type")
		if err != nil {
			log.Fatalf("Error getting instance type: %v", err)
		}

		region, err := cmd.Flags().GetString("region")
		if err != nil {
			log.Fatalf("Error getting region: %v", err)
		}

		provider, err := cmd.Flags().GetString("provider")
		if err != nil {
			log.Fatalf("Error getting provider: %v", err)
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

		taskNames, err := cmd.Flags().GetStringSlice("tasks")
		if err != nil {
			log.Fatalf("Error getting tasks flag: %v", err)
		}

		// Validate region requirement
		if region == "" && !wait {
			log.Fatal("Region is required when not using --wait flag. Use --region to specify a region or --wait to auto-select.")
		}

		// If API token not provided via flag, get from environment based on provider
		if apiToken == "" {
			if provider == "orgo" {
				apiToken = remote.GetAPITokenForProvider(remote.CloudProviderOrgo)
				if apiToken == "" {
					log.Fatal("API token not provided via --api-token flag or ORGO_API_KEY environment variable")
				}
			} else if provider == "modal" {
				apiToken = os.Getenv("MODAL_TOKEN_ID")
				secret := os.Getenv("MODAL_TOKEN_SECRET")
				if apiToken == "" || secret == "" {
					log.Fatal("API token not provided via --api-token flag or MODAL_TOKEN_ID and MODAL_TOKEN_SECRET environment variables")
				}
			} else {
				apiToken = remote.GetAPITokenForProvider(remote.CloudProviderLambda)
				if apiToken == "" {
					log.Fatal("API token not provided via --api-token flag or LAMBDA_API_KEY environment variable")
				}
			}
		}

		// Create HTTP client
		httpClient := remote.NewHTTPClient()

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

		var launchedInstances []remote.LaunchedInstance
		var launchErr error
		var actualRegion = region

		// For Modal provider, skip availability polling and launch directly
		if wait && provider == "modal" {
			fmt.Printf("Launching Modal sandbox (availability check skipped for Modal)...\n")
			goto launchInstance
		}

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

					regions, err := remote.CheckInstanceTypeAvailability(httpClient, apiToken, instanceType)
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
		// Create or retrieve filesystem if flag is provided (only for Lambda)
		if filesystemName != "" && provider != "orgo" {
			// Check if filesystem already exists
			existingFilesystems, err := remote.ListFilesystems(httpClient, apiToken)
			if err != nil {
				log.Fatalf("Error listing filesystems: %v", err)
			}

			var foundFS *remote.Filesystem
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
				if err := remote.SaveFilesystemInfo(filesystemName, foundFS.ID, foundFS.Region.Name); err != nil {
					log.Fatalf("Error saving filesystem info: %v", err)
				}

			} else {
				fmt.Printf("Creating filesystem '%s' in region '%s'...\n", filesystemName, actualRegion)
				fs, err := remote.CreateFilesystem(httpClient, apiToken, filesystemName, actualRegion)
				if err != nil {
					log.Fatalf("Error creating filesystem: %v", err)
				}
				fmt.Printf("Filesystem '%s' created successfully (ID: %s)\n", fs.Name, fs.ID)
			}
		} else if filesystemName != "" && provider == "orgo" {
			fmt.Println("Warning: Orgo provider does not support filesystems. Ignoring --filesystem flag.")
		} else if filesystemName != "" && provider == "modal" {
			fmt.Println("Note: Modal provider uses Volumes instead of filesystems. Use --volume flag for Modal.")
		}

		fmt.Printf("\nLaunching instance...\n")
		if provider == "orgo" {
			// Orgo provider - use project-based launch
			launchedInstances, launchErr = cloudProvider.LaunchInstance(httpClient, apiToken, instanceType, actualRegion, 1, instanceName, "", filesystemName)
			if launchErr == nil && wait {
				// For Orgo, wait for the computer to be ready
				if len(launchedInstances) > 0 {
					fmt.Printf("Waiting for Orgo computer to be ready...\n")
					launchErr = cloudProvider.WaitForInstanceReady(httpClient, apiToken, launchedInstances[0].ID, waitTimeout)
					if launchErr == nil {
						fmt.Printf("Orgo computer is ready!\n")
					}
				}
			}
		} else if provider == "modal" {
			// Modal provider - use Modal Sandboxes
			if wait {
				launchedInstances, launchErr = cloudProvider.LaunchAndWait(httpClient, apiToken, instanceType, actualRegion, 1, instanceName, "", waitTimeout, filesystemName)
			} else {
				launchedInstances, launchErr = cloudProvider.LaunchInstance(httpClient, apiToken, instanceType, actualRegion, 1, instanceName, "", filesystemName)
			}
			if launchErr == nil {
				fmt.Printf("Modal Sandbox launched: %s\n", launchedInstances[0].ID)
			}
		} else {
			// Lambda provider - use region-based launch
			if wait {
				// Launch and wait for instances to be ready
				launchedInstances, launchErr = cloudProvider.LaunchAndWait(httpClient, apiToken, instanceType, actualRegion, 1, instanceName, "", waitTimeout, filesystemName)
			} else {
				// Launch the instance (this creates SSH key and saves to config)
				launchedInstances, launchErr = cloudProvider.LaunchInstance(httpClient, apiToken, instanceType, actualRegion, 1, instanceName, "", filesystemName)
				fmt.Println("Note: SSH config not updated because IP is not yet available. Use --wait flag to update SSH config automatically.")
			}
		}

		// If successful and we waited, update SSH config (only for Lambda)
		if launchErr == nil && wait && provider != "orgo" && provider != "modal" {
			for _, instance := range launchedInstances {
				// Get instance details to get IP
				details, err := remote.GetInstance(httpClient, apiToken, instance.ID)
				if err == nil && details.IP != "" {
					if err := remote.UpdateSSHConfig(instanceName, details.IP, instance.SSHKeyFile); err != nil {
						fmt.Printf("Warning: Failed to update SSH config: %v\n", err)
					}
				}
			}
		}

		// Save SSH key to database (so we can look up file paths later)
		// Note: Instance state is managed by Lambda API, not local DB
		if launchErr == nil {
			database, dbErr := db.InitDB()
			if dbErr != nil {
				fmt.Printf("Warning: Failed to initialize database: %v\n", dbErr)
			} else {
				defer database.Close()
				for _, instance := range launchedInstances {
					// Save SSH key to database (maps key name -> file path)
					if instance.SSHKeyName != "" && instance.SSHKeyFile != "" {
						sshKey := &db.SSHKey{
							Name:       instance.SSHKeyName,
							PrivateKey: instance.SSHKeyFile,
						}
						if saveErr := database.SaveSSHKey(sshKey); saveErr != nil {
							fmt.Printf("Warning: Failed to save SSH key to database: %v\n", saveErr)
						} else {
							fmt.Printf("SSH key %s registered in local database\n", instance.SSHKeyName)
						}
					}
				}
			}
		}

		if launchErr != nil {
			log.Fatalf("Error launching instance: %v", launchErr)
		}

		// Print instance info with SSH access details
		for _, instance := range launchedInstances {
			if provider == "modal" {
				fmt.Printf("Modal Sandbox: %s\n", instance.ID)
				fmt.Printf("Name: %s\n", instanceName)
				if wait {
					fmt.Printf("\nSandbox is ready!\n")
					fmt.Printf("Execute commands with: gotoni run %s\n", instance.ID)
				}
			} else {
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
			}
			fmt.Println()
		}

		// Execute tasks if specified (only for Lambda provider)
		if len(taskNames) > 0 && wait && provider != "orgo" && provider != "modal" {
			if len(launchedInstances) == 0 {
				log.Fatal("No instances launched, cannot execute tasks")
			}

			// Get instance details for IP
			instanceDetails, err := remote.GetInstance(httpClient, apiToken, launchedInstances[0].ID)
			if err != nil {
				log.Fatalf("Failed to get instance details for task execution: %v", err)
			}

			if instanceDetails.IP == "" {
				log.Fatal("Instance has no IP address, cannot execute tasks")
			}

			fmt.Printf("\n=== Executing Tasks ===\n\n")

			// Load all tasks from database
			allTasks, err := remote.ListTasks()
			if err != nil {
				log.Fatalf("Failed to load tasks: %v", err)
			}

			// Build task map
			taskMap := make(map[string]remote.Task)
			for _, task := range allTasks {
				taskMap[task.Name] = task
			}

			// Select tasks by name in order
			var selectedTasks []remote.Task
			for _, taskName := range taskNames {
				task, found := taskMap[taskName]
				if !found {
					log.Fatalf("Task '%s' not found. Use 'gotoni tasks list' to see available tasks.", taskName)
				}
				selectedTasks = append(selectedTasks, task)
			}

			// Create SSH client manager
			manager := remote.NewSSHClientManager()
			defer manager.CloseAllConnections()

			// Connect to instance
			fmt.Printf("Connecting to instance %s via SSH...\n", instanceDetails.IP)
			if err := manager.ConnectToInstance(instanceDetails.IP, launchedInstances[0].SSHKeyFile); err != nil {
				log.Fatalf("Failed to connect via SSH: %v", err)
			}
			fmt.Printf("Connected!\n\n")

			// Execute tasks
			if err := remote.ExecuteTasks(manager, instanceDetails.IP, selectedTasks); err != nil {
				log.Fatalf("Failed to execute tasks: %v", err)
			}

			fmt.Printf("\n✓ All tasks completed successfully!\n")
		} else if len(taskNames) > 0 && (!wait || provider == "orgo" || provider == "modal") {
			if !wait {
				fmt.Println("\nWarning: Tasks specified but --wait flag not set. Tasks will not be executed.")
				fmt.Println("Use --wait flag to execute tasks after instance is ready.")
			} else if provider == "orgo" {
				fmt.Println("\nWarning: Task execution not supported for Orgo provider. Tasks will be skipped.")
			} else if provider == "modal" {
				fmt.Println("\nWarning: Task execution not supported for Modal provider. Tasks will be skipped.")
			}
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
	for key := range remote.MatchingInstanceTypes {
		instanceOptions = append(instanceOptions, key)
	}

	// Extract region keys from the map
	launchCmd.Flags().StringP("provider", "p", "lambda", "Cloud provider to use (lambda, orgo, or modal)")

	launchCmd.Flags().StringP("api-token", "a", "", "API token for cloud provider (can also be set via LAMBDA_API_KEY, ORGO_API_KEY, or MODAL_TOKEN_ID/MODAL_TOKEN_SECRET env vars)")

	launchCmd.Flags().StringP("region", "r", "", "Region (Lambda/Modal) or Project (Orgo) to launch the instance in")

	launchCmd.Flags().StringP("instance-type", "t", "", `choose the instance type to launch.
For Lambda: `+strings.Join(instanceOptions, ", ")+`
For Orgo: 2gb, 4gb, 8gb (RAM-based instance types)
For Modal: h100, a100, a10, t4, cpu
	`)

	launchCmd.Flags().BoolP("wait", "w", false, "Wait for instance type to become available before launching, then wait for instance to be ready")

	launchCmd.Flags().DurationP("poll-interval", "", 5*time.Second, "Polling interval when waiting for instance availability (only used with --wait)")

	launchCmd.Flags().DurationP("poll-timeout", "", 0, "Maximum time to poll for instance availability (0 = no timeout, only used with --wait)")

	launchCmd.Flags().DurationP("wait-timeout", "", 10*time.Minute, "Timeout for waiting for instance to become ready after launching")

	launchCmd.Flags().StringP("filesystem", "f", "", "Create and mount a filesystem with the specified name (will be created in the same region as the instance)")

	launchCmd.Flags().StringSlice("tasks", []string{}, "Comma-separated list of task names to execute after launch (requires --wait)")

	launchCmd.MarkFlagRequired("instance-type")
	// Region is required unless --wait is used (which auto-selects region)
	// We'll validate this in the Run function
}
