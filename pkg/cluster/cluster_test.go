package serve

import (
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/atoniolo76/gotoni/pkg/remote"
	"github.com/joho/godotenv"
)

// TestClusterSGLang tests SGLang server deployment and load balancer strategies on a cluster.
// The cluster is set up once and reused across strategy subtests.
func TestClusterSGLang(t *testing.T) {
	fmt.Println("=== Testing Cluster SGLang Server Deployment ===")

	if err := godotenv.Load("../../.env"); err != nil {
		t.Logf("Warning: Could not load .env file: %v", err)
	}

	httpClient := remote.NewHTTPClient()
	apiToken := remote.GetAPIToken()
	if apiToken == "" {
		t.Skip("LAMBDA_API_KEY not set, skipping integration test")
	}

	// Setup cluster with SGLang running (one-time)
	cluster, err := SetupSGLangCluster(httpClient, apiToken, "sglang-auto-cluster", os.Getenv("HF_TOKEN"))
	if err != nil {
		t.Fatalf("Failed to setup SGLang cluster: %v", err)
	}
	defer cluster.Disconnect()

	// Setup Loki for observability (required for event traces in Grafana)
	fmt.Println("\nSetting up Loki for observability...")
	loggingInstance := cluster.Instances[0]
	fmt.Printf("Using instance %s (%s) for Loki logging\n", loggingInstance.Name, loggingInstance.ID[:16])

	// Note: Loki setup would go here, but it's not implemented in the new structure yet

	// Build gotoni binary on all instances (one-time)
	if err := BuildGotoniOnCluster(cluster); err != nil {
		t.Fatalf("Failed to build gotoni on cluster: %v", err)
	}

	// Test each LB strategy on the same running cluster
	for _, strategy := range []string{"gorgo", "least-loaded"} {
		t.Run("strategy-"+strategy, func(t *testing.T) {
			if err := cluster.DeployLBStrategy(strategy); err != nil {
				t.Fatalf("Failed to deploy %s: %v", strategy, err)
			}
			verifyLBHealthy(t, cluster)
		})
	}

	// Wait a moment for all load balancers to fully initialize
	fmt.Println("\nWaiting 5 seconds for load balancers to fully initialize...")
	time.Sleep(5 * time.Second)

	// 11. Verify tokenizer on remote instances (critical for GORGO policy)
	// Note: Tokenizer verification moved to pkg/tokenizer/tokenizer_test.go to avoid import cycle
	fmt.Println("\n11. Skipping tokenizer verification (run tokenizer tests separately)")

	fmt.Println("=== Cluster SGLang Test Complete ===")
}

// verifyLBHealthy checks that the load balancer is responding on port 8000 for all cluster instances.
func verifyLBHealthy(t *testing.T, cluster *Cluster) {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}

	for _, inst := range cluster.Instances {
		healthURL := fmt.Sprintf("http://%s:8000/lb/health", inst.IP)
		resp, err := client.Get(healthURL)
		if err != nil {
			t.Errorf("LB health check failed for %s: %v", inst.IP, err)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("LB health check returned %d for %s", resp.StatusCode, inst.IP)
		} else {
			fmt.Printf("LB healthy on %s\n", inst.IP)
		}
	}
}
