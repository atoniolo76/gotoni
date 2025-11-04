/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"log"
	"toni/gotoni/pkg/client"

	"github.com/spf13/cobra"
)

// connectCmd represents the connect command
var connectCmd = &cobra.Command{
	Use:   "connect [instance-ip]",
	Short: "Connect to a remote instance via SSH",
	Long:  `Connect to a remote instance using SSH with the key saved in config.`,
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) != 1 {
			log.Fatal("Instance IP is required. Usage: gotoni connect <instance-ip>")
		}

		instanceIP := args[0]

		if err := client.ConnectToInstance(instanceIP); err != nil {
			log.Fatalf("Failed to connect to instance: %v", err)
		}
	},
}

func init() {
	rootCmd.AddCommand(connectCmd)
}
