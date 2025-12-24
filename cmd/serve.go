package cmd

import (
	"fmt"
	"log"

	"github.com/atoniolo76/gotoni/pkg/client"

	"github.com/spf13/cobra"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Serve your favorite models and processes on vLLM across instances and regions",
	Long:  "Serve your favorite models and processeson vLLM across instances and regions.",
	Run: func(cmd *cobra.Command, args []string) {
		apiToken, err := cmd.Flags().GetString("api-token")
		if err != nil {
			log.Fatalf("Error getting API token: %v", err)
		}

		if apiToken == "" {
			apiToken = client.GetAPIToken()
			if apiToken == "" {
				log.Fatal("API token not provided via --api-token flag or LAMBDA_API_KEY environment variable")
			}
		}

		httpClient := client.NewHTTPClient()
		runningInstances, err := client.ListRunningInstances(httpClient, apiToken)

		if len(runningInstances) == 0 {
			fmt.Println("No running instances found.")
			return
		}

		manager := client.SSHClientManager{}

		client.ExecuteTask(SSH)
	},
}
