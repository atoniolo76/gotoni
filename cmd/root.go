/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "gotoni",
	Short: "CLI for automating Lambda.ai cloud instances",
	Long: `gotoni is a CLI tool designed to automate the management of Lambda.ai cloud instances.
It provides a streamlined workflow for launching instances, managing filesystems and SSH keys,
and automating setup tasks.

Key Features:
  - Launch instances with automatic waiting and filesystem attachment
  - Securely share SSH access with team members via Magic Wormhole
  - Connect to instances via SSH or open them directly in VS Code / Cursor
  - Manage filesystems and SSH keys
  - Monitor GPU usage and system logs
  - Run automated provision tasks

Use "gotoni [command] --help" for more information about a command.`,
	// Uncomment the following line if your bare application
	// has an action associated with it:
	// Run: func(cmd *cobra.Command, args []string) { },
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	// Here you will define your flags and configuration settings.
	// Cobra supports persistent flags, which, if defined here,
	// will be global for your application.

	// rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.gotoni.yaml)")

	// Cobra also supports local flags, which will only run
	// when this action is called directly.
	rootCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}
