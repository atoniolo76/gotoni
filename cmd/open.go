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

	"github.com/spf13/cobra"
)

// openCmd represents the open command
var openCmd = &cobra.Command{
	Use:   "open <instance-name> [remote-path]",
	Short: "Open a VS Code window connected to the instance via SSH",
	Long: `Open a new VS Code window automatically connected to the specified instance using Remote-SSH.
This requires the instance to be configured in your SSH config (which 'gotoni launch --wait' does automatically)
and VS Code with the Remote-SSH extension installed.`,
	Args: cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		instanceName := args[0]
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

		// Construct the URI
		// Format: vscode-remote://ssh-remote+<HOST_ALIAS>/path/to/project
		// Note: Cursor uses the same URI scheme "vscode-remote://" for compatibility with VS Code extensions
		uri := fmt.Sprintf("vscode-remote://ssh-remote+%s%s", instanceName, remotePath)

		fmt.Printf("Opening Cursor for instance '%s' at path '%s'...\n", instanceName, remotePath)
		
		// Prepare the command
		// We prefer 'cursor', but fallback to 'code' if 'cursor' is not found
		binary := "cursor"
		if _, err := exec.LookPath("cursor"); err != nil {
			fmt.Println("Warning: 'cursor' command not found in PATH. Falling back to 'code'...")
			binary = "code"
		}

		command := exec.Command(binary, "--folder-uri", uri)
		
		// Attach stdout/stderr to see output if any (though code usually returns immediately or detaches)
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr

		if err := command.Run(); err != nil {
			log.Fatalf("Failed to open %s: %v\nMake sure '%s' is in your PATH.", binary, err, binary)
		}

		fmt.Printf("%s launched successfully.\n", binary)
	},
}

func init() {
	rootCmd.AddCommand(openCmd)
}

