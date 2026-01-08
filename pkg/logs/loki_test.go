/*
Copyright © 2025 ALESSIO TONIOLO

loki_test.go contains tests for the local Loki service
*/
package logs

import (
	"context"
	"testing"
	"time"
)

func TestLokiService(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	loki := NewLokiService()

	// Test starting Loki
	t.Log("Starting Loki service...")
	if err := loki.Start(ctx); err != nil {
		t.Fatalf("Failed to start Loki service: %v", err)
	}

	// Verify it's running
	if !loki.IsRunning() {
		t.Fatal("Loki service should be running")
	}
	t.Log("Loki service is running")

	// Give it some time to initialize
	time.Sleep(5 * time.Second)

	// Test querying logs (should be empty initially)
	t.Log("Testing log queries...")
	entries, err := loki.GetAllLogs(ctx, 10)
	if err != nil {
		t.Fatalf("Failed to query logs: %v", err)
	}

	if len(entries) > 0 {
		t.Logf("Found %d existing log entries", len(entries))
		loki.DisplayLogs(entries)
	} else {
		t.Log("No log entries found (expected for fresh Loki instance)")
	}

	// Test stopping Loki
	t.Log("Stopping Loki service...")
	if err := loki.Stop(); err != nil {
		t.Fatalf("Failed to stop Loki service: %v", err)
	}

	// Verify it's stopped
	if loki.IsRunning() {
		t.Fatal("Loki service should be stopped")
	}
	t.Log("Loki service stopped successfully")
}

// Example usage (commented out for now)
/*
func ExampleLokiWithAlloyCollectors() {
	ctx := context.Background()
	loki := NewLokiService()

	// Start Loki
	if err := loki.Start(ctx); err != nil {
		panic(err)
	}
	defer loki.Stop()

	// Wait for Alloy collectors to send logs
	time.Sleep(30 * time.Second)

	// Query logs with instance metadata
	fmt.Println("=== All Logs ===")
	allLogs, _ := loki.GetAllLogs(ctx, 20)
	loki.DisplayLogs(allLogs)

	// Query logs for specific instance
	fmt.Println("\n=== Instance-specific Logs ===")
	instanceLogs, _ := loki.GetInstanceLogs(ctx, "llama-australia", 10)
	loki.DisplayLogs(instanceLogs)
}
*/
