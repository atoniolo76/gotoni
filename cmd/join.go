/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/atoniolo76/gotoni/pkg/spicedb"
	"github.com/psanford/wormhole-william/wormhole"
	"github.com/spf13/cobra"
)

var joinCmd = &cobra.Command{
	Use:   "join <wormhole-code>",
	Short: "Join an organization using an invite code",
	Long: `Accept an invitation to join an organization. The invite code is
provided by an admin who ran 'gotoni admin org invite'.

This saves your identity locally and configures your org context.

Example:
  gotoni join 7-crossword-revenge`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		code := args[0]

		var c wormhole.Client
		ctx := context.Background()

		fmt.Println("Connecting...")

		msg, err := c.Receive(ctx, code)
		if err != nil {
			log.Fatalf("Receive: %v", err)
		}

		buf := make([]byte, 4096)
		n, _ := msg.Read(buf)

		var payload invitePayload
		if err := json.Unmarshal(buf[:n], &payload); err != nil {
			log.Fatalf("Invalid invite payload: %v", err)
		}

		if err := spicedb.SaveIdentity(&spicedb.Identity{
			UserID: payload.UserID,
			OrgID:  payload.OrgID,
		}); err != nil {
			log.Fatalf("SaveIdentity: %v", err)
		}

		fmt.Printf("\nJoined organization '%s' as %s.\n", payload.OrgID, payload.Role)
		fmt.Printf("Your user ID: %s\n", payload.UserID)
		if payload.Nickname != "" {
			fmt.Printf("Your nickname: %s\n", payload.Nickname)
		}
		fmt.Println("Identity saved locally. You're ready to go.")
	},
}

func init() {
	rootCmd.AddCommand(joinCmd)
}
