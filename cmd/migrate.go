/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"fmt"
	"log"
	"os"

	"github.com/atoniolo76/gotoni/pkg/client"
	"github.com/atoniolo76/gotoni/pkg/db"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// Legacy structures for migration
type LegacyFilesystemInfo struct {
	ID     string `yaml:"id"`
	Region string `yaml:"region"`
}

type LegacyConfig struct {
	Instances   map[string]string               `yaml:"instances,omitempty"`   // instance-id -> ssh-key-name
	SSHKeys     map[string]string               `yaml:"ssh_keys,omitempty"`    // ssh-key-name -> private-key-file
	Filesystems map[string]LegacyFilesystemInfo `yaml:"filesystems,omitempty"` // filesystem-name -> filesystem-info
}

// migrateCmd represents the migrate command
var migrateCmd = &cobra.Command{
	Use:   "migrate-yaml",
	Short: "Migrate legacy YAML configuration to SQLite",
	Long:  `Migrates data from .gotoni/config.yaml to the new SQLite database.`,
	Run: func(cmd *cobra.Command, args []string) {
		configPath := ".gotoni/config.yaml"

		// Check if file exists
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			log.Fatalf("Legacy config file not found at %s", configPath)
		}

		data, err := os.ReadFile(configPath)
		if err != nil {
			log.Fatalf("Failed to read config file: %v", err)
		}

		var config LegacyConfig
		if err := yaml.Unmarshal(data, &config); err != nil {
			log.Fatalf("Failed to parse config: %v", err)
		}

		// Initialize DB
		database, err := db.InitDB()
		if err != nil {
			log.Fatalf("Failed to init db: %v", err)
		}
		defer database.Close()

		fmt.Println("Migrating data...")

		// 1. Migrate SSH Keys
		for name, path := range config.SSHKeys {
			fmt.Printf("Importing SSH Key: %s\n", name)
			if err := database.SaveSSHKey(&db.SSHKey{Name: name, PrivateKey: path}); err != nil {
				log.Printf("Warning: Failed to save SSH key %s: %v", name, err)
			}
		}

		// 2. Migrate Filesystems
		for name, fs := range config.Filesystems {
			fmt.Printf("Importing Filesystem: %s\n", name)
			if err := database.SaveFilesystem(&db.Filesystem{Name: name, ID: fs.ID, Region: fs.Region}); err != nil {
				log.Printf("Warning: Failed to save filesystem %s: %v", name, err)
			}
		}

		// 3. Migrate Instances
		// Note: We only have ID -> KeyName mapping. We don't have full instance details.
		// We'll check cloud for details or store partial records.
		httpClient := client.NewHTTPClient()
		apiToken := client.GetAPIToken()
		
		for id, keyName := range config.Instances {
			fmt.Printf("Importing Instance: %s\n", id)
			
			var inst db.Instance
			inst.ID = id
			inst.SSHKeyName = keyName

			// Try to fetch current details if we have a token
			if apiToken != "" {
				details, err := client.GetInstance(httpClient, apiToken, id)
				if err == nil {
					inst.Name = details.Name
					inst.IPAddress = details.IP
					inst.Status = details.Status
					inst.Region = details.Region.Name
					inst.InstanceType = details.InstanceType.Name
				} else {
					fmt.Printf("  (Could not fetch details from cloud: %v)\n", err)
				}
			}

			if err := database.SaveInstance(&inst); err != nil {
				log.Printf("Warning: Failed to save instance %s: %v", id, err)
			}
		}

		fmt.Println("\nMigration completed successfully!")
		fmt.Println("You can now rename or delete .gotoni/config.yaml")
	},
}

func init() {
	rootCmd.AddCommand(migrateCmd)
}

