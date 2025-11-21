/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"fmt"
	"log"
	"os"

	"github.com/spf13/cobra"
)

// llmCmd represents the llm command
var llmCmd = &cobra.Command{
	Use:   "llm",
	Short: "Display LLM documentation",
	Long:  `Display the contents of the llms.txt file containing LLM documentation.`,
	Run: func(cmd *cobra.Command, args []string) {
		content, err := os.ReadFile("llms.txt")
		if err != nil {
			log.Fatalf("Failed to read llms.txt: %v", err)
		}

		fmt.Print(string(content))
	},
}

func init() {
	rootCmd.AddCommand(llmCmd)
}
