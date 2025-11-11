/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"fmt"
	"log"
	"os"
	"strings"

	"toni/gotoni/pkg/client"

	"github.com/spf13/cobra"
)

// filesystemsCmd represents the filesystems command
var filesystemsCmd = &cobra.Command{
	Use:   "filesystems",
	Short: "Manage filesystems on Lambda Cloud",
	Long:  `List and delete filesystems on Lambda Cloud.`,
}

// filesystemsListCmd represents the list subcommand
var filesystemsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List filesystems",
	Long:  `List all filesystems in your Lambda Cloud account.`,
	Run: func(cmd *cobra.Command, args []string) {
		apiToken, err := cmd.Flags().GetString("api-token")
		if err != nil {
			log.Fatalf("Error getting API token: %v", err)
		}

		// Create HTTP client
		httpClient := client.NewHTTPClient()

		// If API token not provided via flag, get from environment
		if apiToken == "" {
			apiToken = os.Getenv("LAMBDA_API_KEY")
			if apiToken == "" {
				log.Fatal("API token not provided via --api-token flag or LAMBDA_API_KEY environment variable")
			}
		}

		filesystems, err := client.ListFilesystems(httpClient, apiToken)
		if err != nil {
			log.Fatalf("Error listing filesystems: %v", err)
		}

		if len(filesystems) == 0 {
			fmt.Println("No filesystems found.")
			return
		}

		fmt.Printf("Found %d filesystem(s):\n\n", len(filesystems))
		for _, fs := range filesystems {
			fmt.Printf("ID: %s\n", fs.ID)
			fmt.Printf("Name: %s\n", fs.Name)
			fmt.Printf("Region: %s (%s)\n", fs.Region.Name, fs.Region.Description)
			fmt.Printf("Mount Point: %s\n", fs.MountPoint)
			fmt.Printf("Created: %s\n", fs.Created)
			fmt.Printf("In Use: %v\n", fs.IsInUse)
			if fs.BytesUsed != nil {
				bytesUsed := float64(*fs.BytesUsed)
				var sizeStr string
				if bytesUsed < 1024 {
					sizeStr = fmt.Sprintf("%.0f B", bytesUsed)
				} else if bytesUsed < 1024*1024 {
					sizeStr = fmt.Sprintf("%.2f KB", bytesUsed/1024)
				} else if bytesUsed < 1024*1024*1024 {
					sizeStr = fmt.Sprintf("%.2f MB", bytesUsed/(1024*1024))
				} else {
					sizeStr = fmt.Sprintf("%.2f GB", bytesUsed/(1024*1024*1024))
				}
				fmt.Printf("Bytes Used: %s\n", sizeStr)
			}
			if fs.CreatedBy != nil {
				fmt.Printf("Created By: %s (%s)\n", fs.CreatedBy.Email, fs.CreatedBy.ID)
			}
			fmt.Println("---")
		}
	},
}

// filesystemsDeleteCmd represents the delete subcommand
var filesystemsDeleteCmd = &cobra.Command{
	Use:   "delete [filesystem-ids...]",
	Short: "Delete filesystems",
	Long:  `Delete one or more filesystems from Lambda Cloud by providing their IDs.`,
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

		// Get filesystem IDs from args or flags
		var filesystemIDs []string

		if len(args) > 0 {
			// Filesystem IDs provided as arguments
			filesystemIDs = args
		} else {
			// Check for filesystem IDs flag
			idsFlag, err := cmd.Flags().GetStringSlice("ids")
			if err != nil {
				log.Fatalf("Error getting filesystem IDs: %v", err)
			}
			filesystemIDs = idsFlag
		}

		if len(filesystemIDs) == 0 {
			log.Fatal("No filesystem IDs provided. Use 'gotoni filesystems delete <filesystem-id>' or 'gotoni filesystems delete --ids <id1,id2>'")
		}

		// Create HTTP client
		httpClient := client.NewHTTPClient()

		fmt.Printf("Deleting filesystem(s): %s\n", strings.Join(filesystemIDs, ", "))

		var deletedCount int
		for _, filesystemID := range filesystemIDs {
			if err := client.DeleteFilesystem(httpClient, apiToken, filesystemID); err != nil {
				log.Printf("Error deleting filesystem %s: %v", filesystemID, err)
				continue
			}
			fmt.Printf("Successfully deleted filesystem: %s\n", filesystemID)
			deletedCount++
		}

		if deletedCount > 0 {
			fmt.Printf("\nSuccessfully deleted %d filesystem(s)\n", deletedCount)
		}
	},
}

func init() {
	rootCmd.AddCommand(filesystemsCmd)

	// Add list subcommand
	filesystemsCmd.AddCommand(filesystemsListCmd)
	filesystemsListCmd.Flags().StringP("api-token", "a", "", "API token for Lambda Cloud (can also be set via LAMBDA_API_KEY env var)")

	// Add delete subcommand
	filesystemsCmd.AddCommand(filesystemsDeleteCmd)
	filesystemsDeleteCmd.Flags().StringP("api-token", "a", "", "API token for Lambda Cloud (can also be set via LAMBDA_API_KEY env var)")
	filesystemsDeleteCmd.Flags().StringSliceP("ids", "i", []string{}, "Filesystem IDs to delete (can also be provided as arguments)")
}
