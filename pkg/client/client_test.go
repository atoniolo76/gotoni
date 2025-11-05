package client

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLaunchInstanceWithExistingSSHKey(t *testing.T) {
	// Change to project root directory so relative paths work
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	// If we're in pkg/client, go up two levels to project root
	if strings.HasSuffix(wd, "/pkg/client") {
		projectRoot := filepath.Dir(filepath.Dir(wd))
		if err := os.Chdir(projectRoot); err != nil {
			t.Fatalf("Failed to change to project root: %v", err)
		}
		defer os.Chdir(wd) // Restore original directory
	}

	httpClient := &http.Client{
		Timeout:   time.Duration(30) * time.Second,
		Transport: &http.Transport{},
	}

	apiToken := os.Getenv("LAMBDA_API_KEY")
	if apiToken == "" {
		t.Fatal("LAMBDA_API_KEY environment variable is not set")
	}

	sshKeyName := "lambda-key-1762234839" // Matches ssh/lambda-key-1762234839.pem

	launchedInstances, err := LaunchInstance(httpClient, apiToken, "gpu_1x_a10", "us-east-1", 1, "test-launch", sshKeyName)
	if err != nil {
		t.Fatalf("Failed to launch instance: %v", err)
	}

	if len(launchedInstances) == 0 {
		t.Error("Expected at least one launched instance, got none")
	}

	for _, inst := range launchedInstances {
		t.Logf("Launched Instance: ID=%s, SSHKeyName=%s, SSHKeyFile=%s", inst.ID, inst.SSHKeyName, inst.SSHKeyFile)
	}

	time.Sleep(10 * time.Second)

	terminatedResponse, err := TerminateInstance(httpClient, apiToken, []string{launchedInstances[0].ID})
	if err != nil {
		t.Fatalf("Failed to terminate instance: %v", err)
	}

	t.Logf("Terminated Instance: ID=%s, Name=%s, Status=%s", terminatedResponse.TerminatedInstances[0].ID, terminatedResponse.TerminatedInstances[0].Name, terminatedResponse.TerminatedInstances[0].Status)
}

func TestVLLMPlaybook(t *testing.T) {
	// Change to project root directory so relative paths work
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	// If we're in pkg/client, go up two levels to project root
	if strings.HasSuffix(wd, "/pkg/client") {
		projectRoot := filepath.Dir(filepath.Dir(wd))
		if err := os.Chdir(projectRoot); err != nil {
			t.Fatalf("Failed to change to project root: %v", err)
		}
		defer os.Chdir(wd) // Restore original directory
	}

	httpClient := &http.Client{
		Timeout:   time.Duration(30) * time.Second,
		Transport: &http.Transport{},
	}

	apiToken := GetAPIToken()
	if apiToken == "" {
		t.Fatal("API token not found in config.yaml or LAMBDA_API_KEY environment variable")
	}

	// List running instances and use the first one
	t.Logf("Listing running instances...")
	runningInstances, err := ListRunningInstances(httpClient, apiToken)
	if err != nil {
		t.Fatalf("Failed to list running instances: %v", err)
	}

	if len(runningInstances) == 0 {
		t.Fatal("No running instances found. Please launch an instance first.")
	}

	// Use hardcoded instance ID or first running instance
	hardcodedInstanceID := "25fcd7d752a746d389629f6a2e98e6c0"
	var instanceID string
	if hardcodedInstanceID != "" {
		instanceID = hardcodedInstanceID
		t.Logf("Using hardcoded instance: %s", instanceID)
	} else {
		instanceID = runningInstances[0].ID
		t.Logf("Using instance: %s", instanceID)
	}

	// Get instance details
	t.Logf("Getting instance details...")
	instanceDetails, err := GetInstance(httpClient, apiToken, instanceID)
	if err != nil {
		t.Fatalf("Failed to get instance details: %v", err)
	}

	if instanceDetails.IP == "" {
		t.Fatal("Instance IP address is empty")
	}

	t.Logf("Instance IP: %s", instanceDetails.IP)

	// Get SSH key file
	sshKeyFile, err := GetSSHKeyForInstance(instanceID)
	if err != nil {
		t.Fatalf("Failed to get SSH key file: %v", err)
	}
	t.Logf("SSH key file: %s", sshKeyFile)

	// Create SSH client manager and connect
	t.Logf("Creating SSH client manager...")
	manager := NewSSHClientManager()
	defer manager.CloseAllConnections()

	t.Logf("Connecting to instance %s via SSH...", instanceDetails.IP)
	if err := manager.ConnectToInstance(instanceDetails.IP, sshKeyFile); err != nil {
		t.Fatalf("Failed to connect via SSH: %v", err)
	}
	t.Logf("Successfully connected to instance via SSH!")

	// Load config to get tasks
	config, err := LoadConfig()
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if len(config.Tasks) == 0 {
		t.Fatal("No tasks defined in config.yaml")
	}

	// Filter setup tasks (command type)
	setupTasks := FilterTasksByType(config.Tasks, "command")
	if len(setupTasks) == 0 {
		t.Fatal("No command tasks found in config.yaml")
	}

	// Execute setup tasks synchronously
	t.Logf("Executing %d setup task(s) from config.yaml...", len(setupTasks))
	if err := ExecuteTasks(manager, instanceDetails.IP, setupTasks); err != nil {
		t.Fatalf("Failed to execute setup tasks: %v", err)
	}
	t.Logf("Setup tasks completed successfully!")

	// Filter service tasks
	serviceTasks := FilterTasksByType(config.Tasks, "service")
	if len(serviceTasks) == 0 {
		t.Fatal("No service tasks found in config.yaml")
	}

	// Start service in background
	t.Logf("Starting %d service task(s) from config.yaml...", len(serviceTasks))
	if err := ExecuteTasks(manager, instanceDetails.IP, serviceTasks); err != nil {
		t.Fatalf("Failed to start service: %v", err)
	}
	t.Logf("Service started in tmux session!")

	// Wait 15 seconds for service to start
	t.Logf("Waiting 15 seconds for vLLM server to start...")
	time.Sleep(15 * time.Second)

	// Read and encode the image file
	imagePath := "IMG_0009.jpg"
	if _, err := os.Stat(imagePath); os.IsNotExist(err) {
		t.Fatalf("Image file %s not found", imagePath)
	}

	imageData, err := os.ReadFile(imagePath)
	if err != nil {
		t.Fatalf("Failed to read image file: %v", err)
	}

	// Base64 encode the image
	imageBase64 := base64.StdEncoding.EncodeToString(imageData)

	// Upload image to remote instance
	t.Logf("Uploading image to remote instance...")
	// Create a temporary file with base64 encoded image
	uploadCmd := fmt.Sprintf("echo '%s' | base64 -d > /tmp/test_image.jpg", imageBase64)
	output, err := manager.ExecuteCommand(instanceDetails.IP, uploadCmd)
	if err != nil {
		t.Fatalf("Failed to upload image: %v\nOutput: %s", err, output)
	}
	t.Logf("Image uploaded successfully")

	// Now we need to encode the image again for the API request
	// Make curl request to OpenAI-compatible API
	t.Logf("Making request to vLLM API...")

	// Create the request payload
	requestPayload := map[string]interface{}{
		"model": "deepseek-ai/DeepSeek-OCR",
		"messages": []map[string]interface{}{
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type": "image_url",
						"image_url": map[string]string{
							"url": fmt.Sprintf("data:image/jpeg;base64,%s", imageBase64),
						},
					},
					{
						"type": "text",
						"text": "Free OCR.",
					},
				},
			},
		},
		"max_tokens":  2048,
		"temperature": 0.0,
		"extra_body": map[string]interface{}{
			"skip_special_tokens": false,
			"vllm_xargs": map[string]interface{}{
				"ngram_size":          30,
				"window_size":         90,
				"whitelist_token_ids": []int{128821, 128822},
			},
		},
	}

	payloadJSON, err := json.Marshal(requestPayload)
	if err != nil {
		t.Fatalf("Failed to marshal request payload: %v", err)
	}

	// Make HTTP request to the vLLM server
	apiURL := fmt.Sprintf("http://%s:8000/v1/chat/completions", instanceDetails.IP)
	req, err := http.NewRequest("POST", apiURL, strings.NewReader(string(payloadJSON)))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer EMPTY")

	client := &http.Client{
		Timeout: 60 * time.Second,
	}

	startTime := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request to vLLM API: %v", err)
	}
	defer resp.Body.Close()

	responseTime := time.Since(startTime)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}

	// Parse response
	var apiResponse map[string]interface{}
	if err := json.Unmarshal(body, &apiResponse); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	// Extract the generated text
	var generatedText string
	if choices, ok := apiResponse["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if message, ok := choice["message"].(map[string]interface{}); ok {
				if content, ok := message["content"].(string); ok {
					generatedText = content
				}
			}
		}
	}

	separator := strings.Repeat("=", 78)
	t.Logf("%s", separator)
	t.Logf("Response received in %.2f seconds", responseTime.Seconds())
	t.Logf("Generated OCR text:")
	t.Logf("%s", generatedText)
	t.Logf("%s", separator)
	fmt.Printf("\n=== OCR Response ===\n")
	fmt.Printf("Response time: %.2f seconds\n", responseTime.Seconds())
	fmt.Printf("Generated text:\n%s\n", generatedText)
	fmt.Printf("===================\n\n")

	if generatedText == "" {
		t.Error("Generated text is empty")
	}
}
