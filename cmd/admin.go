/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/atoniolo76/gotoni/pkg/spicedb"
	"github.com/google/uuid"
	"github.com/psanford/wormhole-william/wormhole"
	"github.com/spf13/cobra"
)

// --- Root -------------------------------------------------------------------

var adminCmd = &cobra.Command{
	Use:   "admin",
	Short: "Manage access control (organizations, projects, users)",
	Long: `Admin commands for managing organizations, projects, and user roles
via SpiceDB. Requires SPICEDB_ENDPOINT and SPICEDB_TOKEN to be set.

Users are referenced by nickname (stored locally in ~/.config/gotoni/contacts.json).
Nicknames are assigned when inviting users. You can also pass raw UUIDs.

Examples:
  gotoni admin org create acme
  gotoni admin org invite acme alice editor
  gotoni admin org set-role acme alice reader
  gotoni admin org list-users acme
  gotoni admin whoami`,
}

// --- Whoami -----------------------------------------------------------------

var adminWhoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Show current user identity and accessible resources",
	Run: func(cmd *cobra.Command, args []string) {
		client := mustClient()
		ctx := context.Background()
		userID := mustUserID()

		fmt.Printf("User: %s\n\n", userID)

		orgs, err := client.LookupResources(ctx, "organization", "read_access", "user", userID)
		if err != nil {
			log.Fatalf("LookupResources: %v", err)
		}
		if len(orgs) == 0 {
			fmt.Println("Not a member of any organization.")
			return
		}
		fmt.Println("Organizations:")
		for _, org := range orgs {
			role := resolveOrgRole(ctx, client, org, userID)
			fmt.Printf("  %s  (%s)\n", org, role)
		}

		projects, err := client.LookupResources(ctx, "project", "view", "user", userID)
		if err == nil && len(projects) > 0 {
			fmt.Println("\nProjects:")
			for _, p := range projects {
				role := resolveProjectRole(ctx, client, p, userID)
				fmt.Printf("  %s  (%s)\n", p, role)
			}
		}

		resources, err := client.LookupResources(ctx, "resource", "view", "user", userID)
		if err == nil && len(resources) > 0 {
			fmt.Printf("\nResources: %d viewable\n", len(resources))
		}
	},
}

// --- Org --------------------------------------------------------------------

var adminOrgCmd = &cobra.Command{
	Use:   "org",
	Short: "Manage organizations",
}

var adminOrgCreateCmd = &cobra.Command{
	Use:   "create <org-id>",
	Short: "Create an organization and set the current user as admin",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client := mustClient()
		ctx := context.Background()
		orgID := args[0]
		userID := ensureIdentity()

		if err := client.WriteRelationship(ctx, "organization", orgID, "admin", "user", userID); err != nil {
			log.Fatalf("WriteRelationship: %v", err)
		}

		id := spicedb.LoadIdentity()
		if id != nil {
			id.OrgID = orgID
			if err := spicedb.SaveIdentity(id); err != nil {
				log.Printf("Warning: could not save org to identity: %v", err)
			}
		}

		fmt.Printf("Organization '%s' created. You (%s) are admin.\n", orgID, userID)
	},
}

type invitePayload struct {
	OrgID    string `json:"org_id"`
	Role     string `json:"role"`
	UserID   string `json:"user_id"`
	Nickname string `json:"nickname"`
}

var adminOrgInviteCmd = &cobra.Command{
	Use:   "invite <org-id> <nickname> <role>",
	Short: "Invite a user to an organization via magic wormhole",
	Long: `Invite a user by assigning them a local nickname and role.
The nickname is saved in your local contacts for future reference.

Roles: admin, editor, reader`,
	Args: cobra.ExactArgs(3),
	Run: func(cmd *cobra.Command, args []string) {
		client := mustClient()
		orgID, nickname, role := args[0], args[1], args[2]

		if !isValidOrgRole(role) {
			log.Fatalf("Invalid role '%s'. Must be one of: admin, editor, reader", role)
		}

		newUserID := uuid.New().String()
		payload := invitePayload{OrgID: orgID, Role: role, UserID: newUserID, Nickname: nickname}
		jsonData, err := json.Marshal(payload)
		if err != nil {
			log.Fatalf("json.Marshal: %v", err)
		}

		var c wormhole.Client
		ctx := context.Background()

		fmt.Printf("Generating invite for '%s' (%s)...\n", nickname, role)

		reader := strings.NewReader(string(jsonData))
		code, status, err := c.SendFile(ctx, "gotoni-invite.json", reader)
		if err != nil {
			log.Fatalf("SendFile: %v", err)
		}

		fmt.Printf("\nShare this invite code:\n\n\t%s\n\n", code)
		fmt.Println("Waiting for user to accept...")

		select {
		case s := <-status:
			if s.Error != nil {
				log.Fatalf("Transfer failed: %v", s.Error)
			}
			if s.OK {
				if err := client.WriteRelationship(context.Background(), "organization", orgID, role, "user", newUserID); err != nil {
					log.Fatalf("WriteRelationship: %v", err)
				}
				if err := spicedb.SetContact(nickname, newUserID); err != nil {
					log.Printf("Warning: could not save contact: %v", err)
				}
				fmt.Printf("\n'%s' (%s) added as %s of organization '%s'.\n", nickname, newUserID, role, orgID)
			}
		case <-time.After(10 * time.Minute):
			log.Fatal("Invite timed out after 10 minutes.")
		}
	},
}

var adminOrgSetRoleCmd = &cobra.Command{
	Use:   "set-role <org-id> <name-or-id> <new-role>",
	Short: "Change a user's role in an organization",
	Long: `Atomically removes all existing org roles for the user and assigns the new one.
Accepts a nickname (from your contacts) or a raw UUID.

Roles: admin, editor, reader`,
	Args: cobra.ExactArgs(3),
	Run: func(cmd *cobra.Command, args []string) {
		client := mustClient()
		ctx := context.Background()
		orgID, nameOrID, newRole := args[0], args[1], args[2]
		userID := spicedb.ResolveContact(nameOrID)

		if !isValidOrgRole(newRole) {
			log.Fatalf("Invalid role '%s'. Must be one of: admin, editor, reader", newRole)
		}

		for _, role := range []string{"admin", "editor", "reader"} {
			_ = client.DeleteRelationship(ctx, "organization", orgID, role, "user", userID)
		}
		if err := client.WriteRelationship(ctx, "organization", orgID, newRole, "user", userID); err != nil {
			log.Fatalf("WriteRelationship: %v", err)
		}
		fmt.Printf("Set %s to %s on organization '%s'.\n", formatUser(nameOrID, userID), newRole, orgID)
	},
}

var adminOrgAddUserCmd = &cobra.Command{
	Use:   "add-user <org-id> <name-or-id> <role>",
	Short: "Add a user to an organization (roles: admin, editor, reader)",
	Args:  cobra.ExactArgs(3),
	Run: func(cmd *cobra.Command, args []string) {
		client := mustClient()
		ctx := context.Background()
		orgID, nameOrID, role := args[0], args[1], args[2]
		userID := spicedb.ResolveContact(nameOrID)

		if !isValidOrgRole(role) {
			log.Fatalf("Invalid role '%s'. Must be one of: admin, editor, reader", role)
		}
		if err := client.WriteRelationship(ctx, "organization", orgID, role, "user", userID); err != nil {
			log.Fatalf("WriteRelationship: %v", err)
		}
		fmt.Printf("Added %s as %s of organization '%s'.\n", formatUser(nameOrID, userID), role, orgID)
	},
}

var adminOrgRemoveUserCmd = &cobra.Command{
	Use:   "remove-user <org-id> <name-or-id> <role>",
	Short: "Remove a user from an organization role",
	Args:  cobra.ExactArgs(3),
	Run: func(cmd *cobra.Command, args []string) {
		client := mustClient()
		ctx := context.Background()
		orgID, nameOrID, role := args[0], args[1], args[2]
		userID := spicedb.ResolveContact(nameOrID)

		if err := client.DeleteRelationship(ctx, "organization", orgID, role, "user", userID); err != nil {
			log.Fatalf("DeleteRelationship: %v", err)
		}
		fmt.Printf("Removed %s from %s of organization '%s'.\n", formatUser(nameOrID, userID), role, orgID)
	},
}

var adminOrgListUsersCmd = &cobra.Command{
	Use:   "list-users <org-id>",
	Short: "List users in an organization by role",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client := mustClient()
		ctx := context.Background()
		orgID := args[0]

		for _, role := range []string{"admin", "editor", "reader"} {
			perm := role + "_access"
			users, err := client.LookupSubjects(ctx, "organization", orgID, perm, "user")
			if err != nil {
				log.Fatalf("LookupSubjects: %v", err)
			}
			if len(users) > 0 {
				fmt.Printf("%ss:\n", role)
				for _, u := range users {
					fmt.Printf("  %s\n", formatUserByUUID(u))
				}
			}
		}
	},
}

// --- Project ----------------------------------------------------------------

var adminProjectCmd = &cobra.Command{
	Use:   "project",
	Short: "Manage projects",
}

var adminProjectCreateCmd = &cobra.Command{
	Use:   "create <org-id> <project-id>",
	Short: "Create a project within an organization",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		client := mustClient()
		ctx := context.Background()
		orgID, projectID := args[0], args[1]

		if err := client.WriteRelationship(ctx, "project", projectID, "org", "organization", orgID); err != nil {
			log.Fatalf("WriteRelationship: %v", err)
		}
		fmt.Printf("Project '%s' created in organization '%s'.\n", projectID, orgID)
	},
}

var adminProjectSetRoleCmd = &cobra.Command{
	Use:   "set-role <project-id> <name-or-id> <new-role>",
	Short: "Change a user's role in a project",
	Long: `Atomically removes all existing project roles for the user and assigns the new one.
Accepts a nickname (from your contacts) or a raw UUID.

Roles: editor, reader`,
	Args: cobra.ExactArgs(3),
	Run: func(cmd *cobra.Command, args []string) {
		client := mustClient()
		ctx := context.Background()
		projectID, nameOrID, newRole := args[0], args[1], args[2]
		userID := spicedb.ResolveContact(nameOrID)

		if !isValidProjectRole(newRole) {
			log.Fatalf("Invalid role '%s'. Must be one of: editor, reader", newRole)
		}

		for _, role := range []string{"editor", "reader"} {
			_ = client.DeleteRelationship(ctx, "project", projectID, role, "user", userID)
		}
		if err := client.WriteRelationship(ctx, "project", projectID, newRole, "user", userID); err != nil {
			log.Fatalf("WriteRelationship: %v", err)
		}
		fmt.Printf("Set %s to %s on project '%s'.\n", formatUser(nameOrID, userID), newRole, projectID)
	},
}

var adminProjectAddUserCmd = &cobra.Command{
	Use:   "add-user <project-id> <name-or-id> <role>",
	Short: "Add a user to a project (roles: editor, reader)",
	Args:  cobra.ExactArgs(3),
	Run: func(cmd *cobra.Command, args []string) {
		client := mustClient()
		ctx := context.Background()
		projectID, nameOrID, role := args[0], args[1], args[2]
		userID := spicedb.ResolveContact(nameOrID)

		if !isValidProjectRole(role) {
			log.Fatalf("Invalid role '%s'. Must be one of: editor, reader", role)
		}
		if err := client.WriteRelationship(ctx, "project", projectID, role, "user", userID); err != nil {
			log.Fatalf("WriteRelationship: %v", err)
		}
		fmt.Printf("Added %s as %s of project '%s'.\n", formatUser(nameOrID, userID), role, projectID)
	},
}

var adminProjectRemoveUserCmd = &cobra.Command{
	Use:   "remove-user <project-id> <name-or-id> <role>",
	Short: "Remove a user from a project role",
	Args:  cobra.ExactArgs(3),
	Run: func(cmd *cobra.Command, args []string) {
		client := mustClient()
		ctx := context.Background()
		projectID, nameOrID, role := args[0], args[1], args[2]
		userID := spicedb.ResolveContact(nameOrID)

		if err := client.DeleteRelationship(ctx, "project", projectID, role, "user", userID); err != nil {
			log.Fatalf("DeleteRelationship: %v", err)
		}
		fmt.Printf("Removed %s from %s of project '%s'.\n", formatUser(nameOrID, userID), role, projectID)
	},
}

var adminProjectListUsersCmd = &cobra.Command{
	Use:   "list-users <project-id>",
	Short: "List users in a project",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client := mustClient()
		ctx := context.Background()
		projectID := args[0]

		for _, entry := range []struct{ role, perm string }{
			{"editor", "edit"},
			{"reader", "view"},
		} {
			users, err := client.LookupSubjects(ctx, "project", projectID, entry.perm, "user")
			if err != nil {
				log.Fatalf("LookupSubjects: %v", err)
			}
			if len(users) > 0 {
				fmt.Printf("%ss:\n", entry.role)
				for _, u := range users {
					fmt.Printf("  %s\n", formatUserByUUID(u))
				}
			}
		}
	},
}

// --- Helpers ----------------------------------------------------------------

func mustClient() *spicedb.Client {
	client, err := spicedb.NewClient()
	if err != nil {
		log.Fatalf("SpiceDB not configured: %v\nSet SPICEDB_ENDPOINT and SPICEDB_TOKEN.", err)
	}
	return client
}

func mustUserID() string {
	userID := spicedb.ResolveUserID()
	if userID == "" {
		log.Fatal("No identity found. Run 'gotoni admin org create' or 'gotoni admin join' first.")
	}
	return userID
}

func ensureIdentity() string {
	if userID := spicedb.ResolveUserID(); userID != "" {
		return userID
	}
	id, err := spicedb.GenerateIdentity()
	if err != nil {
		log.Fatalf("GenerateIdentity: %v", err)
	}
	fmt.Printf("Generated identity: %s\n", id.UserID)
	return id.UserID
}

func isValidOrgRole(role string) bool {
	return role == "admin" || role == "editor" || role == "reader"
}

func isValidProjectRole(role string) bool {
	return role == "editor" || role == "reader"
}

func resolveOrgRole(ctx context.Context, client *spicedb.Client, orgID, userID string) string {
	if ok, _ := client.CheckPermission(ctx, "organization", orgID, "admin_access", "user", userID); ok {
		return "admin"
	}
	if ok, _ := client.CheckPermission(ctx, "organization", orgID, "write_access", "user", userID); ok {
		return "editor"
	}
	return "reader"
}

func resolveProjectRole(ctx context.Context, client *spicedb.Client, projectID, userID string) string {
	if ok, _ := client.CheckPermission(ctx, "project", projectID, "edit", "user", userID); ok {
		return "editor"
	}
	return "reader"
}

// formatUser shows "nickname (uuid)" if input was a nickname, or just the uuid.
func formatUser(nameOrID, resolvedID string) string {
	if nameOrID != resolvedID {
		return fmt.Sprintf("%s (%s)", nameOrID, resolvedID)
	}
	return resolvedID
}

// formatUserByUUID shows "nickname (uuid)" if a contact exists, or just the uuid.
func formatUserByUUID(userID string) string {
	if name := spicedb.NicknameForUUID(userID); name != "" {
		return fmt.Sprintf("%s (%s)", name, userID)
	}
	return userID
}

// --- Registration -----------------------------------------------------------

func init() {
	rootCmd.AddCommand(adminCmd)

	adminCmd.AddCommand(adminWhoamiCmd)

	adminCmd.AddCommand(adminOrgCmd)
	adminOrgCmd.AddCommand(adminOrgCreateCmd)
	adminOrgCmd.AddCommand(adminOrgInviteCmd)
	adminOrgCmd.AddCommand(adminOrgSetRoleCmd)
	adminOrgCmd.AddCommand(adminOrgAddUserCmd)
	adminOrgCmd.AddCommand(adminOrgRemoveUserCmd)
	adminOrgCmd.AddCommand(adminOrgListUsersCmd)

	adminCmd.AddCommand(adminProjectCmd)
	adminProjectCmd.AddCommand(adminProjectCreateCmd)
	adminProjectCmd.AddCommand(adminProjectSetRoleCmd)
	adminProjectCmd.AddCommand(adminProjectAddUserCmd)
	adminProjectCmd.AddCommand(adminProjectRemoveUserCmd)
	adminProjectCmd.AddCommand(adminProjectListUsersCmd)
}
