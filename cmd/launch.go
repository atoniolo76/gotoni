/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"fmt"
	"strings"
	"toni/gotoni/pkg/client"

	"github.com/spf13/cobra"
)

// launchCmd represents the launch command
var launchCmd = &cobra.Command{
	Use:   "launch",
	Short: "Launch a new instance on your Neocloud.",
	Long:  `Launch a new instance on your Neocloud.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("launch called")
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

	launchCmd.Flags().StringP("instance-type", "t", "", `choose the instance type to launch. Options:
`+strings.Join(instanceOptions, "\n")+`
	`)
}
