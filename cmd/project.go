/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"context"
	"fmt"
	"log"

	"github.com/atoniolo76/gotoni/pkg/spicedb"
	"github.com/spf13/cobra"
)

var projectCmd = &cobra.Command{
	Use:   "project",
	Short: "Manage active project context and assign resources",
	Long: `Switch between projects, view your active project, or assign
resources to a project. Requires SpiceDB to be configured.

Examples:
  gotoni project use ml-team
  gotoni project status
  gotoni project clear
  gotoni project assign ml-team <instance-id>`,
}

var projectUseCmd = &cobra.Command{
	Use:   "use <project-id>",
	Short: "Set the active project for subsequent commands",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		projectID := args[0]

		if spicedb.Enabled() {
			ctx := context.Background()
			if err := spicedb.Check(ctx, "project", projectID, "view"); err != nil {
				log.Fatalf("You don't have access to project '%s': %v", projectID, err)
			}
		}

		id := spicedb.LoadIdentity()
		if id == nil {
			log.Fatal("No identity found. Run 'gotoni admin org create' or 'gotoni admin join' first.")
		}
		id.ProjectID = projectID
		if err := spicedb.SaveIdentity(id); err != nil {
			log.Fatalf("SaveIdentity: %v", err)
		}
		fmt.Printf("Active project set to '%s'.\n", projectID)
	},
}

var projectClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Clear the active project (revert to org-level scope)",
	Run: func(cmd *cobra.Command, args []string) {
		id := spicedb.LoadIdentity()
		if id == nil {
			log.Fatal("No identity found.")
		}
		id.ProjectID = ""
		if err := spicedb.SaveIdentity(id); err != nil {
			log.Fatalf("SaveIdentity: %v", err)
		}
		fmt.Println("Active project cleared. Resources will be scoped to your organization.")
	},
}

var projectStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the active project and organization",
	Run: func(cmd *cobra.Command, args []string) {
		id := spicedb.LoadIdentity()
		if id == nil {
			fmt.Println("No identity configured.")
			return
		}
		if id.OrgID != "" {
			fmt.Printf("Organization: %s\n", id.OrgID)
		} else {
			fmt.Println("Organization: (none)")
		}
		if id.ProjectID != "" {
			fmt.Printf("Project:      %s\n", id.ProjectID)
		} else {
			fmt.Println("Project:      (none)")
		}
	},
}

var projectAssignCmd = &cobra.Command{
	Use:   "assign <project-id> <instance-id>",
	Short: "Assign an existing resource to a project",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		projectID, instanceID := args[0], args[1]

		if !spicedb.Enabled() {
			log.Fatal("SpiceDB is not configured. Set SPICEDB_ENDPOINT and SPICEDB_TOKEN.")
		}

		ctx := context.Background()
		if err := spicedb.Check(ctx, "project", projectID, "edit"); err != nil {
			log.Fatalf("Permission denied: %v", err)
		}

		client, err := spicedb.NewClient()
		if err != nil {
			log.Fatalf("NewClient: %v", err)
		}
		if err := client.WriteRelationship(ctx, "resource", instanceID, "project", "project", projectID); err != nil {
			log.Fatalf("WriteRelationship: %v", err)
		}
		fmt.Printf("Resource '%s' assigned to project '%s'.\n", instanceID, projectID)
	},
}

func init() {
	rootCmd.AddCommand(projectCmd)
	projectCmd.AddCommand(projectUseCmd)
	projectCmd.AddCommand(projectClearCmd)
	projectCmd.AddCommand(projectStatusCmd)
	projectCmd.AddCommand(projectAssignCmd)
}
