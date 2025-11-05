package client

import (
	"fmt"
	"os"
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

	// Wait for result with timeout
	select {
	case result := <-resultChan:
		return result.Output, result.Error
	case <-time.After(30 * time.Second):
		return "", fmt.Errorf("timeout waiting for result from %s", instanceIP)
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

	output, err := session.CombinedOutput(cmd.Command)
	cmd.Result <- &SSHResult{
		Output: string(output),
		Error:  err,
	}
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
