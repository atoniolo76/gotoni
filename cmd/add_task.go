/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"fmt"
	"log"
	"strings"

	"github.com/AlecAivazis/survey/v2"
	"github.com/atoniolo76/gotoni/pkg/client"
	"github.com/spf13/cobra"
)

// addTaskCmd represents the add-task command
var addTaskCmd = &cobra.Command{
	Use:   "add-task",
	Short: "Add a new task interactively",
	Long: `Add a new task to your configuration interactively.
This command will prompt you for task name, type, command, and dependencies.`,
	Run: func(cmd *cobra.Command, args []string) {
		// Load existing config
		config, err := client.LoadConfig()
		if err != nil {
			log.Fatalf("Failed to load config: %v", err)
		}

		// Initialize tasks slice if nil
		if config.Tasks == nil {
			config.Tasks = []client.Task{}
		}

		// Get existing task names for dependency selection
		existingTaskNames := make([]string, 0)
		for _, task := range config.Tasks {
			existingTaskNames = append(existingTaskNames, task.Name)
		}

		var task client.Task

		// Prompt for task name
		namePrompt := &survey.Input{
			Message: "Task name:",
		}
		if err := survey.AskOne(namePrompt, &task.Name, survey.WithValidator(survey.Required)); err != nil {
			log.Fatalf("Error getting task name: %v", err)
		}

		// Prompt for task type
		typePrompt := &survey.Select{
			Message: "Task type:",
			Options: []string{"command", "service"},
			Default: "command",
		}
		if err := survey.AskOne(typePrompt, &task.Type); err != nil {
			log.Fatalf("Error getting task type: %v", err)
		}

		// Prompt for command
		commandPrompt := &survey.Input{
			Message: "Command to execute:",
		}
		if err := survey.AskOne(commandPrompt, &task.Command, survey.WithValidator(survey.Required)); err != nil {
			log.Fatalf("Error getting command: %v", err)
		}

		// Prompt for dependencies (optional)
		if len(existingTaskNames) > 0 {
			var selectedDeps []string
			depPrompt := &survey.MultiSelect{
				Message: "Select dependencies (tasks that must complete before this one):",
				Options: existingTaskNames,
				Help:    "Use space to select, enter to confirm. Leave empty if no dependencies.",
			}
			if err := survey.AskOne(depPrompt, &selectedDeps); err != nil {
				log.Fatalf("Error getting dependencies: %v", err)
			}
			task.DependsOn = selectedDeps
		} else {
			// If no existing tasks, ask if they want to add dependencies manually
			var depInput string
			depPrompt := &survey.Input{
				Message: "Dependencies (comma-separated task names, or press Enter for none):",
			}
			if err := survey.AskOne(depPrompt, &depInput); err != nil {
				log.Fatalf("Error getting dependencies: %v", err)
			}
			if depInput != "" {
				deps := strings.Split(depInput, ",")
				for i := range deps {
					deps[i] = strings.TrimSpace(deps[i])
				}
				task.DependsOn = deps
			}
		}

		// Optional: Prompt for working directory
		var workingDir string
		wdPrompt := &survey.Input{
			Message: "Working directory (optional, press Enter to skip):",
		}
		if err := survey.AskOne(wdPrompt, &workingDir); err != nil {
			log.Fatalf("Error getting working directory: %v", err)
		}
		if workingDir != "" {
			task.WorkingDir = workingDir
		}

		// Add task to config
		config.Tasks = append(config.Tasks, task)

		// Save config
		if err := client.SaveConfig(config); err != nil {
			log.Fatalf("Failed to save config: %v", err)
		}

		fmt.Printf("\n✓ Task '%s' added successfully!\n", task.Name)
		if len(task.DependsOn) > 0 {
			fmt.Printf("  Dependencies: %s\n", strings.Join(task.DependsOn, ", "))
		}
	},
}

func init() {
	rootCmd.AddCommand(addTaskCmd)
}
