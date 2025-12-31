/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"log"
	"strings"

	"github.com/atoniolo76/gotoni/pkg/remote"
	"github.com/spf13/cobra"
)

// openCmd represents the open command
var openCmd = &cobra.Command{
	Use:   "open <instance-name> [remote-path]",
	Short: "Open a remote instance in VS Code or Cursor",
	Long: `Open a remote instance in VS Code or Cursor via SSH remote. Defaults to Cursor if available.

Requirements:
  - For Cursor: Ensure the 'cursor' command is installed (Cmd+Shift+P > "Install 'cursor' command")
  - For VS Code: Ensure the 'code' command is installed (Cmd+Shift+P > "Install 'code' command")`,
	Args: cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
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

		forceCode, _ := cmd.Flags().GetBool("code")

		// Determine instance name
		var instanceName string
		if strings.Contains(target, ".") {
			// It's an IP - we can't open in IDE with IP, need to resolve to name
			log.Fatal("Cannot open IDE with IP address. Please provide an instance name instead.")
		} else {
			// It's a name - try to resolve it for IDE opening
			// For IDE opening, we ideally need the SSH config name

			// Try to resolve to verify it exists, but use the provided name as the host alias
			apiToken := client.GetAPIToken()
			if apiToken != "" {
				httpClient := client.NewHTTPClient()
				_, err := client.ResolveInstance(httpClient, apiToken, target)
				if err == nil {
					// It exists in cloud, assume it's configured in SSH
					instanceName = target
				} else {
					// Not found in cloud, might be just an SSH config entry
					instanceName = target
				}
			} else {
				instanceName = target
			}
		}

		openInIDE(instanceName, remotePath, forceCode)
	},
}

func init() {
	rootCmd.AddCommand(openCmd)
	openCmd.Flags().Bool("code", false, "Force open in VS Code")
}
