/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	"toni/gotoni/pkg/client"

	"github.com/spf13/cobra"
)

// launchCmd represents the launch command
var launchCmd = &cobra.Command{
	Use:   "launch",
	Short: "Launch a new instance on your Neocloud.",
	Long:  `Launch a new instance on your Neocloud.`,
	Run: func(cmd *cobra.Command, args []string) {
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

		// If API token not provided via flag, get from environment
		if apiToken == "" {
			apiToken = os.Getenv("LAMBDA_API_KEY")
			if apiToken == "" {
				log.Fatal("API token not provided via --api-token flag or LAMBDA_API_KEY environment variable")
			}
		}

		// Create HTTP client with TLS skip verify for testing
		httpClient := &http.Client{
			Timeout: time.Duration(30) * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}

		// Launch the instance (this creates SSH key and saves to config)
		launchedInstances, err := client.LaunchInstance(httpClient, apiToken, instanceType, region, 1, "cli-launch", "")
		if err != nil {
			log.Fatalf("Error launching instance: %v", err)
		}

		// Print instance info with SSH access details
		for _, instance := range launchedInstances {
			fmt.Printf("Launched instance: %s\n", instance.ID)
			fmt.Printf("SSH Key: %s\n", instance.SSHKeyName)
			fmt.Printf("SSH Key File: %s\n", instance.SSHKeyFile)
			fmt.Printf("Connect with: ssh -i %s ubuntu@<instance-ip>\n", instance.SSHKeyFile)
			fmt.Printf("Or use: gotoni connect <instance-ip>\n\n")
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
	launchCmd.Flags().StringP("api-token", "a", "", "API token for Lambda Cloud (can also be set via LAMBDA_API_KEY env var)")

	launchCmd.Flags().StringP("region", "r", "", "Region to launch the instance in (e.g., us-east-1, us-west-2)")

	launchCmd.Flags().StringP("instance-type", "t", "", `choose the instance type to launch. Options:
`+strings.Join(instanceOptions, "\n")+`
	`)

	launchCmd.MarkFlagRequired("instance-type")
	launchCmd.MarkFlagRequired("region")
}
