/*
Copyright © 2025 ALESSIO TONIOLO

build.go contains simple binary build and deployment functionality.
No CGO needed - tokenizer is a separate Rust sidecar.
*/
package serve

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/atoniolo76/gotoni/pkg/remote"
)

const (
	RemoteGotoniPath = "/home/ubuntu/gotoni"
)

// BuildGotoniLinux builds the gotoni binary for Linux amd64
func BuildGotoniLinux(outputPath string) error {
	fmt.Println("Building gotoni for Linux amd64...")

	cmd := exec.Command("go", "build", "-o", outputPath, ".")
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build failed: %w", err)
	}

	info, _ := os.Stat(outputPath)
	fmt.Printf("Built %s (%d MB)\n", outputPath, info.Size()/(1024*1024))
	return nil
}

// DeployGotoniToCluster uploads the gotoni binary to all cluster instances
func DeployGotoniToCluster(cluster *Cluster, binaryPath string) error {
	fmt.Printf("Deploying gotoni to %d instances...\n", len(cluster.Instances))

	var wg sync.WaitGroup
	var failCount int
	var mu sync.Mutex

	for _, inst := range cluster.Instances {
		wg.Add(1)
		go func(instance remote.RunningInstance) {
			defer wg.Done()

			// Get SSH key
			var sshKeyPath string
			if len(instance.SSHKeyNames) > 0 {
				if sshKey, err := cluster.database.GetSSHKey(instance.SSHKeyNames[0]); err == nil {
					sshKeyPath = sshKey.PrivateKey
				}
			}
			if sshKeyPath == "" {
				fmt.Printf("  %s: ❌ no SSH key\n", instance.Name)
				mu.Lock()
				failCount++
				mu.Unlock()
				return
			}

			// SCP upload
			scpCmd := exec.Command("scp",
				"-i", sshKeyPath,
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				binaryPath,
				fmt.Sprintf("ubuntu@%s:%s", instance.IP, RemoteGotoniPath),
			)
			if err := scpCmd.Run(); err != nil {
				fmt.Printf("  %s: ❌ upload failed\n", instance.Name)
				mu.Lock()
				failCount++
				mu.Unlock()
				return
			}

			// Make executable
			chmodCmd := exec.Command("ssh",
				"-i", sshKeyPath,
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				fmt.Sprintf("ubuntu@%s", instance.IP),
				"chmod +x /home/ubuntu/gotoni",
			)
			chmodCmd.Run()

			fmt.Printf("  %s: ✅ uploaded\n", instance.Name)
		}(inst)
	}

	wg.Wait()

	if failCount == len(cluster.Instances) {
		return fmt.Errorf("upload failed on all instances")
	}

	fmt.Printf("Deployed to %d/%d instances\n", len(cluster.Instances)-failCount, len(cluster.Instances))
	return nil
}

// DeployLBStrategy deploys load balancers with the given strategy
func DeployLBStrategy(cluster *Cluster, strategy string) error {
	return DeployLBStrategyWithConfig(cluster, LBDeployConfig{Strategy: strategy})
}

// DeployLBStrategyWithConfig deploys LB with full configuration
func DeployLBStrategyWithConfig(cluster *Cluster, config LBDeployConfig) error {
	fmt.Printf("Deploying LB strategy: %s\n", config.Strategy)

	// Stop existing load balancers
	cluster.ExecuteOnCluster("pkill -f 'gotoni lb' 2>/dev/null || true")
	time.Sleep(2 * time.Second)

	// Collect all peer IPs
	var allIPs []string
	for _, inst := range cluster.Instances {
		allIPs = append(allIPs, inst.IP)
	}

	maxConcurrent := config.MaxConcurrent
	if maxConcurrent == 0 {
		maxConcurrent = 100
	}

	failCount := 0
	for i, inst := range cluster.Instances {
		// Build peer list (all other nodes)
		var peers []string
		for j, peerIP := range allIPs {
			if i != j {
				peers = append(peers, fmt.Sprintf("%s:8000", peerIP))
			}
		}

		// Build command
		lbCommand := fmt.Sprintf("/home/ubuntu/gotoni lb start --listen-port 8000 --local-port 8080 --strategy %s --max-concurrent %d",
			config.Strategy, maxConcurrent)

		// Add node ID
		nodeID := inst.Name
		if nodeID == "" {
			nodeID = fmt.Sprintf("node-%d", i)
		}
		lbCommand += fmt.Sprintf(" --node-id %s", nodeID)

		// Add running threshold if set
		if config.RunningThreshold > 0 {
			lbCommand += fmt.Sprintf(" --running-threshold %d", config.RunningThreshold)
		}

		// Add peers
		for _, peer := range peers {
			lbCommand += fmt.Sprintf(" --peers %s", peer)
		}

		// Start LB
		task := remote.Task{
			Name:       "start gotoni load balancer",
			Command:    lbCommand,
			Background: true,
			WorkingDir: "/home/ubuntu",
		}

		if err := remote.ExecuteTask(cluster.sshMgr, inst.IP, task, make(map[string]bool)); err != nil {
			fmt.Printf("  %s: ❌ %v\n", inst.Name, err)
			failCount++
		} else {
			fmt.Printf("  %s: ✅ LB started with %d peers\n", inst.Name, len(peers))
		}
	}

	if failCount == len(cluster.Instances) {
		return fmt.Errorf("failed to start load balancer on all instances")
	}

	time.Sleep(3 * time.Second)
	return nil
}
