package remote

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

type SSHClientManager struct {
	clients map[string]*SSHClient
	mu      sync.RWMutex
}

type SSHClient struct {
	client     *ssh.Client
	instanceIP string
	keyFile    string

	cmdChan chan *SSHCommand
	done    chan struct{}
}

type SSHCommand struct {
	Command string
	Result  chan *SSHResult
}

type SSHResult struct {
	Error  error
	Output string
}

type ClusterCommandResult struct {
	InstanceID string
	InstanceIP string
	Output     string
	Error      error
}

func NewSSHClientManager() *SSHClientManager {
	return &SSHClientManager{
		clients: make(map[string]*SSHClient),
	}
}

func (scm *SSHClientManager) ConnectToInstance(instanceIP string, keyFile string) error {
	scm.mu.Lock()
	defer scm.mu.Unlock()

	if _, exists := scm.clients[instanceIP]; exists {
		return fmt.Errorf("client already connected to instance %s", instanceIP)
	}

	client, err := createSSHClient(instanceIP, keyFile)
	if err != nil {
		return fmt.Errorf("failed to connect to %s: %w", instanceIP, err)
	}

	sc := &SSHClient{
		client:     client,
		instanceIP: instanceIP,
		keyFile:    keyFile,
		cmdChan:    make(chan *SSHCommand),
		done:       make(chan struct{}),
	}

	go sc.run()

	scm.clients[instanceIP] = sc

	return nil
}

func (scm *SSHClientManager) ExecuteCommand(instanceIP string, command string) (string, error) {
	return scm.ExecuteCommandWithTimeout(instanceIP, command, 30*time.Minute)
}

func (scm *SSHClientManager) ExecuteCommandWithTimeout(instanceIP string, command string, timeout time.Duration) (string, error) {
	scm.mu.RLock()
	client, exists := scm.clients[instanceIP]
	scm.mu.RUnlock()

	if !exists {
		return "", fmt.Errorf("no client connected to instance %s", instanceIP)
	}

	resultChan := make(chan *SSHResult, 1)
	cmd := &SSHCommand{
		Command: command,
		Result:  resultChan,
	}

	// Send command with timeout
	select {
	case client.cmdChan <- cmd:
		// Command sent
	case <-time.After(5 * time.Second):
		return "", fmt.Errorf("timeout sending command to %s", instanceIP)
	}

	// Wait for result with configurable timeout
	select {
	case result := <-resultChan:
		return result.Output, result.Error
	case <-time.After(timeout):
		return "", fmt.Errorf("timeout waiting for result from %s (timeout: %v)", instanceIP, timeout)
	}
}

// DisconnectFromInstance closes connection to a specific instance
func (scm *SSHClientManager) DisconnectFromInstance(instanceIP string) {
	scm.mu.Lock()
	defer scm.mu.Unlock()

	if client, exists := scm.clients[instanceIP]; exists {
		close(client.done)
		client.client.Close()
		delete(scm.clients, instanceIP)
	}
}

// CloseAllConnections closes all SSH connections
func (scm *SSHClientManager) CloseAllConnections() {
	scm.mu.Lock()
	defer scm.mu.Unlock()

	for ip, client := range scm.clients {
		close(client.done)
		client.client.Close()
		delete(scm.clients, ip)
	}
}

// run handles the SSH client's command loop
func (sc *SSHClient) run() {
	defer sc.client.Close()

	for {
		select {
		case cmd := <-sc.cmdChan:
			sc.executeCommand(cmd)
		case <-sc.done:
			return
		}
	}
}

// executeCommand runs a single command over SSH
func (sc *SSHClient) executeCommand(cmd *SSHCommand) {
	session, err := sc.client.NewSession()
	if err != nil {
		cmd.Result <- &SSHResult{Error: fmt.Errorf("session error: %w", err)}
		return
	}
	defer session.Close()

	// Set a very long timeout for the session itself (for long-running commands)
	// The actual timeout is enforced by ExecuteCommandWithTimeout
	output, err := session.CombinedOutput(cmd.Command)
	cmd.Result <- &SSHResult{
		Output: string(output),
		Error:  err,
	}
}

// CreateSystemdService creates a systemd user service file
func (scm *SSHClientManager) CreateSystemdService(instanceIP string, serviceName string, command string, workingDir string, envVars map[string]string, serviceOpts *Task) error {
	// Ensure systemd user directory exists
	serviceDirCmd := "mkdir -p ~/.config/systemd/user"
	if _, err := scm.ExecuteCommand(instanceIP, serviceDirCmd); err != nil {
		return fmt.Errorf("failed to create systemd user directory: %w", err)
	}

	// Build environment variables
	envVarsStr := ""
	for k, v := range envVars {
		// Escape quotes in environment variable values
		escapedV := strings.ReplaceAll(v, `"`, `\"`)
		envVarsStr += fmt.Sprintf("Environment=\"%s=%s\"\n", k, escapedV)
	}

	// Add PATH if not already set
	if _, hasPath := envVars["PATH"]; !hasPath {
		envVarsStr += "Environment=\"PATH=/home/ubuntu/.local/bin:/usr/local/bin:/usr/bin:/bin\"\n"
	}

	// Escape the command for the service file
	// Since ExecStart needs the full command, we'll use a shell wrapper
	execStart := fmt.Sprintf("/bin/bash -c %s", fmt.Sprintf("%q", command))

	// Set defaults for service options
	restart := "always"
	restartSec := 10

	if serviceOpts != nil {
		if serviceOpts.Restart != "" {
			restart = serviceOpts.Restart
		}
		if serviceOpts.RestartSec > 0 {
			restartSec = serviceOpts.RestartSec
		}
	}

	// Build service section
	serviceSection := fmt.Sprintf(`[Service]
Type=simple
WorkingDirectory=%s
%sExecStart=%s
Restart=%s
RestartSec=%d
StandardOutput=journal
StandardError=journal
`, workingDir, envVarsStr, execStart, restart, restartSec)

	// Create service file content
	serviceContent := fmt.Sprintf(`[Unit]
Description=%s
After=network.target

%s
[Install]
WantedBy=default.target
`, serviceName, serviceSection)

	// Write service file using base64 encoding for reliable transfer
	serviceFile := fmt.Sprintf("~/.config/systemd/user/%s.service", serviceName)

	// Base64 encode the content to avoid shell escaping issues
	encodedContent := base64.StdEncoding.EncodeToString([]byte(serviceContent))

	// Decode and write using base64 -d
	writeCmd := fmt.Sprintf("echo %s | base64 -d > %s", encodedContent, serviceFile)
	if _, err := scm.ExecuteCommand(instanceIP, writeCmd); err != nil {
		return fmt.Errorf("failed to write service file: %w", err)
	}

	// Reload systemd daemon
	reloadCmd := "systemctl --user daemon-reload"
	if _, err := scm.ExecuteCommand(instanceIP, reloadCmd); err != nil {
		return fmt.Errorf("failed to reload systemd daemon: %w", err)
	}

	return nil
}

// StartSystemdService starts a systemd user service
func (scm *SSHClientManager) StartSystemdService(instanceIP string, serviceName string) error {
	// Ensure lingering is enabled for user services to persist after logout
	enableCmd := "loginctl enable-linger $USER 2>/dev/null || true"
	scm.ExecuteCommand(instanceIP, enableCmd)

	// Start the service
	startCmd := fmt.Sprintf("systemctl --user start %s.service", serviceName)
	output, err := scm.ExecuteCommand(instanceIP, startCmd)
	if err != nil {
		return fmt.Errorf("failed to start service: %w\nOutput: %s", err, output)
	}
	return nil
}

// StopSystemdService stops a systemd user service
func (scm *SSHClientManager) StopSystemdService(instanceIP string, serviceName string) error {
	stopCmd := fmt.Sprintf("systemctl --user stop %s.service 2>/dev/null || true", serviceName)
	_, err := scm.ExecuteCommand(instanceIP, stopCmd)
	return err
}

// ListSystemdServices lists all gotoni-managed systemd services
func (scm *SSHClientManager) ListSystemdServices(instanceIP string) ([]string, error) {
	// List all user services and filter for gotoni-managed ones
	// We'll use a naming convention: gotoni-<service-name>
	cmd := "systemctl --user list-units --type=service --no-pager --no-legend 2>/dev/null | grep -E 'gotoni-' | awk '{print $1}' | sed 's/\\.service$//' | sed 's/^gotoni-//' || echo ''"
	output, err := scm.ExecuteCommand(instanceIP, cmd)
	if err != nil {
		return nil, err
	}

	services := []string{}
	if output != "" {
		lines := strings.Split(strings.TrimSpace(output), "\n")
		for _, line := range lines {
			if line != "" {
				services = append(services, strings.TrimSpace(line))
			}
		}
	}
	return services, nil
}

// GetSystemdServiceStatus gets the status of a systemd service
func (scm *SSHClientManager) GetSystemdServiceStatus(instanceIP string, serviceName string) (string, error) {
	cmd := fmt.Sprintf("systemctl --user is-active gotoni-%s.service 2>&1 || echo 'inactive'", serviceName)
	status, err := scm.ExecuteCommand(instanceIP, cmd)
	if err != nil {
		return "unknown", err
	}
	return strings.TrimSpace(status), nil
}

// GetSystemdLogs gets logs from a systemd service using journalctl
func (scm *SSHClientManager) GetSystemdLogs(instanceIP string, serviceName string, lines int) (string, error) {
	if lines <= 0 {
		lines = 100 // Default to last 100 lines
	}
	cmd := fmt.Sprintf("journalctl --user -u gotoni-%s.service -n %d --no-pager 2>&1 || echo 'Service not found or no logs'", serviceName, lines)
	return scm.ExecuteCommand(instanceIP, cmd)
}

// Legacy tmux functions (kept for backward compatibility, but deprecated)
// ListTmuxSessions lists all tmux sessions on a remote instance
func (scm *SSHClientManager) ListTmuxSessions(instanceIP string) ([]string, error) {
	// Now delegates to systemd
	return scm.ListSystemdServices(instanceIP)
}

// GetTmuxLogs gets the output/logs from a tmux session
func (scm *SSHClientManager) GetTmuxLogs(instanceIP string, sessionName string, lines int) (string, error) {
	// Now delegates to systemd
	return scm.GetSystemdLogs(instanceIP, sessionName, lines)
}

// AttachToTmuxSession attaches to a tmux session (returns command to run)
func AttachToTmuxSessionCommand(instanceIP string, sessionName string, sshKeyFile string) string {
	return fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null ubuntu@%s -t 'tmux attach -t %s'", sshKeyFile, instanceIP, sessionName)
}

// createSSHClient establishes SSH connection
func createSSHClient(instanceIP, keyFile string) (*ssh.Client, error) {
	keyBytes, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("read key: %w", err)
	}

	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("parse key: %w", err)
	}

	config := &ssh.ClientConfig{
		User: "ubuntu",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	client, err := ssh.Dial("tcp", instanceIP+":22", config)
	if err != nil {
		return nil, fmt.Errorf("dial SSH: %w", err)
	}

	return client, nil
}

// UpdateSSHConfig adds or updates a Host entry in the user's SSH config
func UpdateSSHConfig(hostName, hostIP, identityFile string) error {
	sshDir, err := getSSHDir()
	if err != nil {
		return err
	}

	configPath := filepath.Join(sshDir, "config")

	// Create config file if it doesn't exist
	f, err := os.OpenFile(configPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("failed to open ssh config: %w", err)
	}
	defer f.Close()

	// Read existing config to check for duplicates (basic check)
	content, err := os.ReadFile(configPath)
	if err == nil {
		if strings.Contains(string(content), "Host "+hostName) {
			// Already exists, maybe just print a warning or skip
			// For now, we skip to avoid duplicate entries making the file messy
			fmt.Printf("Warning: Host '%s' already exists in %s, skipping update.\n", hostName, configPath)
			return nil
		}
	}

	// Construct new entry
	// We use StrictHostKeyChecking no to avoid manual verification for new cloud instances
	entry := fmt.Sprintf("\nHost %s\n  HostName %s\n  User ubuntu\n  IdentityFile %s\n  StrictHostKeyChecking no\n  UserKnownHostsFile /dev/null\n", hostName, hostIP, identityFile)

	if _, err := f.WriteString(entry); err != nil {
		return fmt.Errorf("failed to write to ssh config: %w", err)
	}

	fmt.Printf("Updated SSH config at %s with host '%s'\n", configPath, hostName)
	return nil
}
