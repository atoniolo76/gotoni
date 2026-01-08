/*
Copyright © 2025 ALESSIO TONIOLO

example.go contains example usage of the Loki service
*/
package logs

import (
	"context"
	"fmt"
	"log"
	"time"
)

// ExampleLokiUsage demonstrates how to use the Loki service
func ExampleLokiUsage() {
	ctx := context.Background()
	loki := NewLokiService()

	fmt.Println("🚀 Starting Loki service...")
	if err := loki.Start(ctx); err != nil {
		log.Fatalf("Failed to start Loki: %v", err)
	}
	defer func() {
		fmt.Println("🛑 Stopping Loki service...")
		if err := loki.Stop(); err != nil {
			log.Printf("Error stopping Loki: %v", err)
		}
	}()

	fmt.Println("✅ Loki service started successfully")
	fmt.Printf("📊 Loki UI available at: http://localhost:%s\n", loki.config.HTTPPort)

	// Monitor for incoming logs
	fmt.Println("🔍 Monitoring for log entries from Alloy collectors...")
	fmt.Println("   (Alloy collectors on remote instances should be sending logs here)")
	fmt.Println()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Query recent logs
			entries, err := loki.GetAllLogs(ctx, 50)
			if err != nil {
				log.Printf("Error querying logs: %v", err)
				continue
			}

			if len(entries) > 0 {
				fmt.Printf("\n📋 Found %d log entries:\n", len(entries))
				fmt.Println("────────────────────────────────────────────────────────────────")
				loki.DisplayLogs(entries)
				fmt.Println("────────────────────────────────────────────────────────────────")

				// Show instance breakdown
				instances := make(map[string]int)
				for _, entry := range entries {
					if instanceID, ok := entry.Labels["instance_id"]; ok {
						instances[instanceID]++
					}
				}

				if len(instances) > 0 {
					fmt.Println("📊 Logs by instance:")
					for instanceID, count := range instances {
						fmt.Printf("   • %s: %d entries\n", instanceID, count)
					}
				}
			} else {
				fmt.Print("⏳ Waiting for logs from Alloy collectors...\r")
			}
		}
	}
}

// RunExample starts the example Loki service
func RunExample() {
	fmt.Println("🎯 Loki Log Aggregation Example")
	fmt.Println("=================================")
	fmt.Println()
	fmt.Println("This example demonstrates:")
	fmt.Println("• Starting a local Loki instance")
	fmt.Println("• Receiving logs from Alloy collectors on remote instances")
	fmt.Println("• Displaying logs with automatic instance metadata")
	fmt.Println()
	fmt.Println("Make sure your Alloy collectors are configured to send logs to:")
	fmt.Println("  http://localhost:3100/loki/api/v1/push")
	fmt.Println()
	fmt.Println("Press Ctrl+C to stop")
	fmt.Println()

	ExampleLokiUsage()
}
