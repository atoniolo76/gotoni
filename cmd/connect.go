/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/atoniolo76/gotoni/pkg/client"

	"github.com/spf13/cobra"
)

// connectCmd represents the connect command
var connectCmd = &cobra.Command{
	Use:   "connect [instance-name|instance-ip] [remote-path]",
	Short: "Connect to a remote instance",
	Long:  `Connect to a remote instance via SSH or open in an IDE. If an instance name is provided, it will be resolved to an IP or SSH config entry. If an IP is provided, it will connect directly via SSH. Use --cursor or --code to open in VS Code/Cursor instead of SSH.`,
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		cursor, err := cmd.Flags().GetBool("cursor")
		if err != nil {
			log.Fatalf("Error getting cursor flag: %v", err)
		}

		code, err := cmd.Flags().GetBool("code")
		if err != nil {
			log.Fatalf("Error getting code flag: %v", err)
		}

		if cursor && code {
			log.Fatal("Cannot specify both --cursor and --code flags")
		}

		target := args[0]
		remotePath := "/home/ubuntu" // Default path

		if len(args) > 1 {
			inputPath := args[1]
			if strings.HasPrefix(inputPath, "/") {
				remotePath = inputPath
			} else {
				// Treat as relative to home directory
				remotePath = "/home/ubuntu/" + inputPath
			}
		}

		// Determine the instance name for IDE opening
		var instanceName string
		if strings.Contains(target, ".") {
			// It's an IP - we can't open in IDE with IP, need to resolve to name
			if cursor || code {
				log.Fatal("Cannot open IDE with IP address. Please provide an instance name instead.")
			}
			instanceName = target // For SSH, we can use the IP directly
		} else {
			// It's a name - try to resolve it for IDE opening, or use directly for SSH
			if cursor || code {
				// For IDE opening, we need the SSH config name
				instanceName = target
			} else {
				// For SSH, try to resolve to IP first, fallback to name
				apiToken := client.GetAPIToken()
				if apiToken != "" {
					httpClient := client.NewHTTPClient()
					instance, err := client.ResolveInstance(httpClient, apiToken, target)
					if err == nil {
						// Found instance, use IP for SSH
						instanceName = instance.IP
					} else {
						// Not found, assume it's an SSH config entry name
						instanceName = target
					}
				} else {
					// No API token, assume SSH config entry
					instanceName = target
				}
			}
		}

		if cursor || code {
			// Open in IDE
			openInIDE(instanceName, remotePath, cursor)
		} else {
			// Connect via SSH
			if err := client.ConnectToInstance(instanceName); err != nil {
				log.Fatalf("Failed to connect to instance: %v", err)
			}
		}
	},
}

// openInIDE opens the specified instance in VS Code or Cursor
func openInIDE(instanceName, remotePath string, useCursor bool) {
	// Construct the URI
	// Format: vscode-remote://ssh-remote+<HOST_ALIAS>/path/to/project
	// Note: Cursor uses the same URI scheme "vscode-remote://" for compatibility with VS Code extensions
	uri := fmt.Sprintf("vscode-remote://ssh-remote+%s%s", instanceName, remotePath)

	var binary string
	if useCursor {
		binary = "cursor"
		fmt.Printf("Opening Cursor for instance '%s' at path '%s'...\n", instanceName, remotePath)
	} else {
		// Default to 'cursor', fallback to 'code'
		binary = "cursor"
		if _, err := exec.LookPath("cursor"); err != nil {
			fmt.Println("Warning: 'cursor' command not found in PATH. Falling back to 'code'...")
			binary = "code"
		}
		fmt.Printf("Opening %s for instance '%s' at path '%s'...\n", binary, instanceName, remotePath)
	}

	command := exec.Command(binary, "--folder-uri", uri)

	// Attach stdout/stderr to see output if any (though code usually returns immediately or detaches)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr

	if err := command.Run(); err != nil {
		log.Fatalf("Failed to open %s: %v\nMake sure '%s' is in your PATH.", binary, err, binary)
	}

	fmt.Printf("%s launched successfully.\n", binary)
}

func init() {
	rootCmd.AddCommand(connectCmd)
	connectCmd.Flags().Bool("cursor", false, "Open in Cursor IDE instead of SSH")
	connectCmd.Flags().Bool("code", false, "Open in VS Code instead of SSH")
}
