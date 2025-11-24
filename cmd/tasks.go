/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/atoniolo76/gotoni/pkg/client"
	"github.com/atoniolo76/gotoni/pkg/db"
	"github.com/spf13/cobra"
)

// tasksCmd represents the tasks command
var tasksCmd = &cobra.Command{
	Use:   "tasks",
	Short: "Manage automation tasks for instance provisioning",
	Long: `Create, list, and manage automation tasks that can be executed on instances.
Tasks are reusable commands or services that can be run during instance provisioning or via the provision command.`,
}

// tasksAddCmd represents the tasks add subcommand
var tasksAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a new automation task",
	Long: `Add a new task that can be executed on instances. Tasks can be commands or services.
Examples:
  # Add a simple command task
  gotoni tasks add --name "Install Docker" --command "curl -fsSL https://get.docker.com | sh"
  
  # Add a service task that runs in background
  gotoni tasks add --name "Start API Server" --type service --command "python api.py" --working-dir /home/ubuntu/app
  
  # Add a task with dependencies
  gotoni tasks add --name "Start App" --command "npm start" --depends-on "Install Node,Clone Repo"`,
	Run: func(cmd *cobra.Command, args []string) {
		name, _ := cmd.Flags().GetString("name")
		taskType, _ := cmd.Flags().GetString("type")
		command, _ := cmd.Flags().GetString("command")
		background, _ := cmd.Flags().GetBool("background")
		workingDir, _ := cmd.Flags().GetString("working-dir")
		envFlag, _ := cmd.Flags().GetStringToString("env")
		dependsOnFlag, _ := cmd.Flags().GetStringSlice("depends-on")
		whenCondition, _ := cmd.Flags().GetString("when")
		restart, _ := cmd.Flags().GetString("restart")
		restartSec, _ := cmd.Flags().GetInt("restart-sec")

		if name == "" {
			log.Fatal("Task name is required (--name)")
		}
		if command == "" {
			log.Fatal("Task command is required (--command)")
		}

		// Default type is command
		if taskType == "" {
			taskType = "command"
		}

		// Validate type
		if taskType != "command" && taskType != "service" {
			log.Fatal("Task type must be 'command' or 'service'")
		}

		// Init DB
		database, err := db.InitDB()
		if err != nil {
			log.Fatalf("Failed to init database: %v", err)
		}
		defer database.Close()

		// Convert env map to JSON string
		var envJSON string
		if len(envFlag) > 0 {
			envBytes, err := json.Marshal(envFlag)
			if err != nil {
				log.Fatalf("Failed to marshal env: %v", err)
			}
			envJSON = string(envBytes)
		}

		// Convert depends_on to JSON string
		var dependsOnJSON string
		if len(dependsOnFlag) > 0 {
			dependsOnBytes, err := json.Marshal(dependsOnFlag)
			if err != nil {
				log.Fatalf("Failed to marshal depends_on: %v", err)
			}
			dependsOnJSON = string(dependsOnBytes)
		}

		// Create task
		task := &db.Task{
			Name:          name,
			Type:          taskType,
			Command:       command,
			Background:    background,
			WorkingDir:    workingDir,
			Env:           envJSON,
			DependsOn:     dependsOnJSON,
			WhenCondition: whenCondition,
			Restart:       restart,
			RestartSec:    restartSec,
		}

		// Save to database
		if err := database.SaveTask(task); err != nil {
			log.Fatalf("Failed to save task: %v", err)
		}

		fmt.Printf("✓ Task '%s' added successfully!\n", name)
		fmt.Printf("  Type: %s\n", taskType)
		fmt.Printf("  Command: %s\n", command)
		if workingDir != "" {
			fmt.Printf("  Working Dir: %s\n", workingDir)
		}
		if len(dependsOnFlag) > 0 {
			fmt.Printf("  Dependencies: %v\n", dependsOnFlag)
		}
	},
}

// tasksListCmd represents the tasks list subcommand
var tasksListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all automation tasks",
	Long:  `Display all tasks that can be executed on instances.`,
	Run: func(cmd *cobra.Command, args []string) {
		tasks, err := client.ListTasks()
		if err != nil {
			log.Fatalf("Failed to list tasks: %v", err)
		}

		if len(tasks) == 0 {
			fmt.Println("No tasks found.")
			fmt.Println("\nAdd tasks with: gotoni tasks add --name \"Task Name\" --command \"your command\"")
			return
		}

		fmt.Printf("Found %d task(s):\n\n", len(tasks))
		for i, task := range tasks {
			fmt.Printf("%d. %s\n", i+1, task.Name)
			fmt.Printf("   Type: %s\n", task.Type)
			fmt.Printf("   Command: %s\n", task.Command)
			if task.WorkingDir != "" {
				fmt.Printf("   Working Dir: %s\n", task.WorkingDir)
			}
			if len(task.DependsOn) > 0 {
				fmt.Printf("   Depends On: %v\n", task.DependsOn)
			}
			if task.Background {
				fmt.Printf("   Background: true\n")
			}
			if task.When != "" {
				fmt.Printf("   Condition: %s\n", task.When)
			}
			fmt.Println()
		}
	},
}

// tasksDeleteCmd represents the tasks delete subcommand
var tasksDeleteCmd = &cobra.Command{
	Use:   "delete <task-name>",
	Short: "Delete an automation task",
	Long:  `Delete a task by name.`,
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		taskName := args[0]

		database, err := db.InitDB()
		if err != nil {
			log.Fatalf("Failed to init database: %v", err)
		}
		defer database.Close()

		if err := database.DeleteTask(taskName); err != nil {
			log.Fatalf("Failed to delete task: %v", err)
		}

		fmt.Printf("✓ Task '%s' deleted successfully!\n", taskName)
	},
}

func init() {
	rootCmd.AddCommand(tasksCmd)

	// Add subcommands
	tasksCmd.AddCommand(tasksAddCmd)
	tasksCmd.AddCommand(tasksListCmd)
	tasksCmd.AddCommand(tasksDeleteCmd)

	// Flags for tasks add
	tasksAddCmd.Flags().StringP("name", "n", "", "Task name (required)")
	tasksAddCmd.Flags().StringP("type", "t", "command", "Task type: command or service")
	tasksAddCmd.Flags().StringP("command", "c", "", "Command to execute (required)")
	tasksAddCmd.Flags().BoolP("background", "b", false, "Run in background (for command type)")
	tasksAddCmd.Flags().StringP("working-dir", "w", "", "Working directory for command")
	tasksAddCmd.Flags().StringToStringP("env", "e", nil, "Environment variables (key=value)")
	tasksAddCmd.Flags().StringSliceP("depends-on", "d", nil, "Task dependencies (comma-separated task names)")
	tasksAddCmd.Flags().String("when", "", "Condition to run task")
	tasksAddCmd.Flags().String("restart", "always", "Restart policy for services: always, on-failure, on-success, no")
	tasksAddCmd.Flags().Int("restart-sec", 10, "Seconds to wait before restarting service")

	tasksAddCmd.MarkFlagRequired("name")
	tasksAddCmd.MarkFlagRequired("command")
}

