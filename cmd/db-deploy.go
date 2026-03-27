package cmd

import (
	"log"

	"github.com/atoniolo76/gotoni/pkg/spicedb"
	"github.com/spf13/cobra"
)

var dbDeployCmd = &cobra.Command{
	Use:   "db-deploy",
	Short: "Deploy SpiceDB on Modal",
	Long:  "Deploy a SpiceDB instance as a Modal sandbox for centralized authorization.",
	Run:   runDBDeploy,
}

func runDBDeploy(cmd *cobra.Command, args []string) {
	// key, _ := cmd.Flags().GetString("key")
	// if key == "" {
	// 	key = os.Getenv("SPICEDB_PRESHARED_KEY")
	// }
	// if key == "" {
	// 	log.Fatal("Provide a preshared key via --key or SPICEDB_PRESHARED_KEY")
	// }

	if _, err := spicedb.DeploySpicedb(""); err != nil {
		log.Fatalf("deploy: %v", err)
	}
}

func init() {
	rootCmd.AddCommand(dbDeployCmd)
	dbDeployCmd.Flags().String("key", "", "SpiceDB gRPC preshared key (or set SPICEDB_PRESHARED_KEY)")
}
