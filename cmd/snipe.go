package cmd

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/atoniolo76/gotoni/pkg/client"
	"github.com/spf13/cobra"
)

var snipeCmd = &cobra.Command{
	Use:   "snipe",
	Short: "Poll for instance availability and launch when found",
	Long:  "Continuously polls for a specific GPU instance type to become available and automatically launches it when found.",
	Run: func(cmd *cobra.Command, args []string) {
		apiToken, err := cmd.Flags().GetString("api-token")
		if err != nil {
			log.Fatalf("Error getting API token: %v", err)
		}

		// If API token not provided via flag, get from environment
		if apiToken == "" {
			apiToken = os.Getenv("LAMBDA_API_KEY")
			if apiToken == "" {
				log.Fatal("API token not provided via --api-token flag or LAMBDA_API_KEY environment variable")
			}
		}

		instanceType, err := cmd.Flags().GetString("instance-type")
		if err != nil {
			log.Fatalf("Error getting instance type: %v", err)
		}

		pollInterval, err := cmd.Flags().GetDuration("interval")
		if err != nil {
			log.Fatalf("Error getting poll interval: %v", err)
		}

		timeout, err := cmd.Flags().GetDuration("timeout")
		if err != nil {
			log.Fatalf("Error getting timeout: %v", err)
		}

		filesystemName, err := cmd.Flags().GetString("filesystem")
		if err != nil {
			log.Fatalf("Error getting filesystem flag: %v", err)
		}

		wait, err := cmd.Flags().GetBool("wait")
		if err != nil {
			log.Fatalf("Error getting wait flag: %v", err)
		}

		waitTimeout, err := cmd.Flags().GetDuration("wait-timeout")
		if err != nil {
			log.Fatalf("Error getting wait timeout: %v", err)
		}

		httpClient := client.NewHTTPClient()

		fmt.Printf("Sniping instance type: %s\n", instanceType)
		fmt.Printf("Polling every %v\n", pollInterval)
		if timeout > 0 {
			fmt.Printf("Timeout: %v\n", timeout)
		} else {
			fmt.Printf("No timeout (will poll indefinitely)\n")
		}
		fmt.Println()

		// Create ticker for polling
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()

		// Create timeout channel if timeout is set
		var timeoutChan <-chan time.Time
		if timeout > 0 {
			timeoutChan = time.After(timeout)
		}

		// Start polling
		for {
			select {
			case <-timeoutChan:
				log.Fatalf("Timeout reached after %v. Instance type %s did not become available.", timeout, instanceType)

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

					// Always use first available region
					selectedRegion := regions[0].Name
					fmt.Printf("\nUsing region: %s\n", selectedRegion)

					// Launch the instance
					fmt.Printf("\nLaunching instance...\n")
					var launchedInstances []client.LaunchedInstance
					var launchErr error

					if wait {
						launchedInstances, launchErr = client.LaunchAndWait(httpClient, apiToken, instanceType, selectedRegion, 1, "snipe-launch", "", waitTimeout, filesystemName)
					} else {
						launchedInstances, launchErr = client.LaunchInstance(httpClient, apiToken, instanceType, selectedRegion, 1, "snipe-launch", "", filesystemName)
					}

					if launchErr != nil {
						log.Fatalf("Error launching instance: %v", launchErr)
					}

					// Print success message
					fmt.Printf("\n✓ Successfully launched instance(s)!\n")
					for _, instance := range launchedInstances {
						fmt.Printf("  Instance ID: %s\n", instance.ID)
						fmt.Printf("  SSH Key: %s\n", instance.SSHKeyName)
						fmt.Printf("  SSH Key File: %s\n", instance.SSHKeyFile)
						fmt.Printf("  Connect with: ssh -i %s ubuntu@<instance-ip>\n", instance.SSHKeyFile)
						fmt.Printf("  Or use: gotoni connect <instance-ip>\n\n")
					}

					return // Successfully launched, exit
				} else {
					fmt.Printf("  Not available yet. Retrying in %v...\n", pollInterval)
				}
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(snipeCmd)

	snipeCmd.Flags().StringP("api-token", "a", "", "API token for Lambda Cloud (can also be set via LAMBDA_API_KEY env var)")
	snipeCmd.Flags().StringP("instance-type", "t", "", "Type of instance you want to snipe e.g. gpu_1x_gh200")
	snipeCmd.Flags().DurationP("interval", "i", 5*time.Second, "Polling interval (e.g., 5s, 1m)")
	snipeCmd.Flags().DurationP("timeout", "", 0, "Maximum time to poll (0 = no timeout, e.g., 30m, 1h)")
	snipeCmd.Flags().StringP("filesystem", "f", "", "Create and mount a filesystem with the specified name")
	snipeCmd.Flags().BoolP("wait", "w", false, "Wait for instance to become ready before returning")
	snipeCmd.Flags().DurationP("wait-timeout", "", 10*time.Minute, "Timeout for waiting for instance to become ready")

	snipeCmd.MarkFlagRequired("instance-type")
}
