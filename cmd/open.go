/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"context"
	"log"
	"strings"

	"github.com/atoniolo76/gotoni/pkg/remote"
	"github.com/atoniolo76/gotoni/pkg/spicedb"
	"github.com/spf13/cobra"
)

// openCmd represents the open command
var openCmd = &cobra.Command{
	Use:   "open <instance-name> [remote-path] --code|--cursor",
	Short: "Open a remote instance in VS Code or Cursor",
	Long: `Open a remote instance in VS Code or Cursor via SSH remote.

You must specify either --code or --cursor to choose the editor.

Requirements:
  - For Cursor: Ensure the 'cursor' command is installed (Cmd+Shift+P > "Install 'cursor' command")
  - For VS Code: Ensure the 'code' command is installed (Cmd+Shift+P > "Install 'code' command")`,
	Args: cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		useCode, _ := cmd.Flags().GetBool("code")
		useCursor, _ := cmd.Flags().GetBool("cursor")

		if !useCode && !useCursor {
			log.Fatal("You must specify either --code or --cursor")
		}
		if useCode && useCursor {
			log.Fatal("Cannot specify both --code and --cursor")
		}

		target := args[0]
		remotePath := "/home/ubuntu"

		if len(args) > 1 {
			inputPath := args[1]
			if strings.HasPrefix(inputPath, "/") {
				remotePath = inputPath
			} else {
				remotePath = "/home/ubuntu/" + inputPath
			}
		}

		// Determine instance name
		var instanceName string
		if strings.Contains(target, ".") {
			log.Fatal("Cannot open IDE with IP address. Please provide an instance name instead.")
		} else {
			apiToken := remote.GetAPIToken()
			if apiToken != "" {
				httpClient := remote.NewHTTPClient()
				inst, err := remote.ResolveInstance(httpClient, apiToken, target)
				if err == nil {
					if checkErr := spicedb.Check(context.Background(), "resource", inst.ID, "ssh"); checkErr != nil {
						log.Fatalf("Permission denied: %v", checkErr)
					}
					instanceName = target
				} else {
					instanceName = target
				}
			} else {
				instanceName = target
			}
		}

		openInIDE(instanceName, remotePath, useCode)
	},
}

func init() {
	rootCmd.AddCommand(openCmd)
	openCmd.Flags().Bool("code", false, "Open in VS Code")
	openCmd.Flags().Bool("cursor", false, "Open in Cursor")
}
