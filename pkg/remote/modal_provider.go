package remote

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/atoniolo76/gotoni/pkg/db"
	"github.com/modal-labs/libmodal/modal-go"
)

type ModalProvider struct {
	client *modal.Client
}

func NewModalProvider() *ModalProvider {
	return &ModalProvider{}
}

func (p *ModalProvider) getClient() (*modal.Client, error) {
	if p.client != nil {
		return p.client, nil
	}

	client, err := modal.NewClient()
	if err != nil {
		return nil, fmt.Errorf("failed to create Modal client: %w", err)
	}
	p.client = client
	return client, nil
}

func (p *ModalProvider) LaunchInstance(httpClient *http.Client, apiToken string, instanceType string, region string, quantity int, name string, sshKeyName string, filesystemName string) ([]LaunchedInstance, error) {
	return p.LaunchAndWait(httpClient, apiToken, instanceType, region, quantity, name, sshKeyName, 24*time.Hour, filesystemName)
}

func (p *ModalProvider) LaunchAndWait(httpClient *http.Client, apiToken string, instanceType string, region string, quantity int, name string, sshKeyName string, timeout time.Duration, filesystemName string) ([]LaunchedInstance, error) {
	client, err := p.getClient()
	if err != nil {
		return nil, err
	}

	ctx := context.Background()

	gpuType := instanceType
	cpu := 4.0
	memoryMiB := 16384

	switch instanceType {
	case "h100":
		gpuType = "H100"
		cpu = 8.0
		memoryMiB = 32768
	case "a100":
		gpuType = "A100"
		cpu = 8.0
		memoryMiB = 32768
	case "a10":
		gpuType = "A10"
		cpu = 4.0
		memoryMiB = 16384
	case "t4":
		gpuType = "T4"
		cpu = 4.0
		memoryMiB = 16384
	case "cpu":
		gpuType = ""
		cpu = 4.0
		memoryMiB = 8192
	}

	envVars := map[string]string{}
	if filesystemName != "" {
		_, volErr := client.Volumes.FromName(ctx, filesystemName, &modal.VolumeFromNameParams{
			CreateIfMissing: true,
		})
		if volErr == nil {
			envVars["VOLUME_PATH"] = "/modal-volume"
		}
	}

	sandboxParams := &modal.SandboxCreateParams{
		CPU:       cpu,
		MemoryMiB: memoryMiB,
		GPU:       gpuType,
		Timeout:   timeout,
		Env:       envVars,
		Name:      name,
		Command:   []string{"sleep", "infinity"},
		Regions:   []string{region},
	}

	volumes := map[string]*modal.Volume{}
	if filesystemName != "" {
		vol, volErr := client.Volumes.FromName(ctx, filesystemName, &modal.VolumeFromNameParams{
			CreateIfMissing: true,
		})
		if volErr == nil {
			volumes["/modal-volume"] = vol
			sandboxParams.Volumes = volumes
		}
	}

	app, appErr := client.Apps.FromName(ctx, "gotoni", &modal.AppFromNameParams{
		CreateIfMissing: true,
	})
	if appErr != nil {
		app = &modal.App{AppID: "gotoni"}
	}

	image := client.Images.FromRegistry("nvidia/cuda:12.1.0-runtime-ubuntu22.04", nil)

	sandbox, err := client.Sandboxes.Create(ctx, app, image, sandboxParams)
	if err != nil {
		return nil, fmt.Errorf("failed to create Modal Sandbox: %w", err)
	}

	database, dbErr := db.InitDB()
	if dbErr == nil {
		defer database.Close()
		launchedInstance := LaunchedInstance{
			ID:         sandbox.SandboxID,
			SSHKeyName: name,
			SSHKeyFile: "",
		}
		database.SaveInstance(&db.Instance{
			ID:     sandbox.SandboxID,
			Name:   name,
			Status: "running",
		})
		return []LaunchedInstance{launchedInstance}, nil
	}

	return []LaunchedInstance{{
		ID:         sandbox.SandboxID,
		SSHKeyName: name,
		SSHKeyFile: "",
	}}, nil
}

func (p *ModalProvider) GetInstance(httpClient *http.Client, apiToken string, instanceID string) (*RunningInstance, error) {
	client, err := p.getClient()
	if err != nil {
		return nil, err
	}

	ctx := context.Background()

	sandbox, err := client.Sandboxes.FromID(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("failed to get Modal Sandbox: %w", err)
	}

	status := "booting"
	pollResult, pollErr := sandbox.Poll(ctx)
	if pollErr == nil && pollResult == nil {
		status = "running"
	} else if pollResult != nil {
		status = "terminated"
	}

	return &RunningInstance{
		ID:     sandbox.SandboxID,
		Status: status,
		Name:   sandbox.SandboxID,
	}, nil
}

func (p *ModalProvider) ListRunningInstances(httpClient *http.Client, apiToken string) ([]RunningInstance, error) {
	client, err := p.getClient()
	if err != nil {
		return nil, err
	}

	ctx := context.Background()

	sandboxes, err := client.Sandboxes.List(ctx, &modal.SandboxListParams{})
	if err != nil {
		return nil, fmt.Errorf("failed to list Modal Sandboxes: %w", err)
	}

	var instances []RunningInstance
	for sb := range sandboxes {
		pollResult, _ := sb.Poll(ctx)
		if pollResult == nil {
			instances = append(instances, RunningInstance{
				ID:     sb.SandboxID,
				Status: "running",
				Name:   sb.SandboxID,
			})
		}
	}

	return instances, nil
}

func (p *ModalProvider) TerminateInstance(httpClient *http.Client, apiToken string, instanceIDs []string) (*InstanceTerminateResponse, error) {
	if len(instanceIDs) == 0 {
		return nil, fmt.Errorf("at least one instance ID is required")
	}

	client, err := p.getClient()
	if err != nil {
		return nil, err
	}

	ctx := context.Background()

	var terminated []RunningInstance

	for _, id := range instanceIDs {
		sandbox, err := client.Sandboxes.FromID(ctx, id)
		if err != nil {
			continue
		}

		if err := sandbox.Terminate(ctx); err != nil {
			continue
		}

		terminated = append(terminated, RunningInstance{
			ID:     sandbox.SandboxID,
			Status: "terminated",
		})

		database, _ := db.InitDB()
		if database != nil {
			defer database.Close()
			database.DeleteInstance(id)
		}
	}

	return &InstanceTerminateResponse{
		TerminatedInstances: terminated,
	}, nil
}

func (p *ModalProvider) WaitForInstanceReady(httpClient *http.Client, apiToken string, instanceID string, timeout time.Duration) error {
	client, err := p.getClient()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	sandbox, err := client.Sandboxes.FromID(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("failed to get Modal Sandbox: %w", err)
	}

	_, err = sandbox.Wait(ctx)
	return err
}

func (p *ModalProvider) CreateSSHKeyForProject(httpClient *http.Client, apiToken string) (string, string, error) {
	return "", "", fmt.Errorf("SSH keys are not supported for Modal provider - Modal uses token-based authentication")
}

func (p *ModalProvider) ListSSHKeys(httpClient *http.Client, apiToken string) ([]SSHKey, error) {
	return nil, fmt.Errorf("SSH keys are not supported for Modal provider")
}

func (p *ModalProvider) DeleteSSHKey(httpClient *http.Client, apiToken string, sshKeyID string) error {
	return fmt.Errorf("SSH keys are not supported for Modal provider")
}

func (p *ModalProvider) GetAvailableInstanceTypes(httpClient *http.Client, apiToken string) ([]Instance, error) {
	return []Instance{
		{
			InstanceType: InstanceType{
				Name:        "h100",
				Description: "H100 GPU",
			},
			RegionsWithCapacityAvailable: []Region{
				{Name: "us-east-1", Description: "US East (N. Virginia)"},
				{Name: "us-west-2", Description: "US West (Oregon)"},
				{Name: "eu-west-1", Description: "EU (Ireland)"},
			},
		},
		{
			InstanceType: InstanceType{
				Name:        "a100",
				Description: "A100 GPU",
			},
			RegionsWithCapacityAvailable: []Region{
				{Name: "us-east-1", Description: "US East (N. Virginia)"},
				{Name: "us-west-2", Description: "US West (Oregon)"},
			},
		},
		{
			InstanceType: InstanceType{
				Name:        "a10",
				Description: "A10G GPU",
			},
			RegionsWithCapacityAvailable: []Region{
				{Name: "us-east-1", Description: "US East (N. Virginia)"},
				{Name: "us-west-2", Description: "US West (Oregon)"},
				{Name: "eu-west-1", Description: "EU (Ireland)"},
			},
		},
		{
			InstanceType: InstanceType{
				Name:        "t4",
				Description: "T4 GPU",
			},
			RegionsWithCapacityAvailable: []Region{
				{Name: "us-east-1", Description: "US East (N. Virginia)"},
				{Name: "us-west-2", Description: "US West (Oregon)"},
			},
		},
		{
			InstanceType: InstanceType{
				Name:        "cpu",
				Description: "CPU only",
			},
			RegionsWithCapacityAvailable: []Region{
				{Name: "us-east-1", Description: "US East (N. Virginia)"},
				{Name: "us-west-2", Description: "US West (Oregon)"},
				{Name: "eu-west-1", Description: "EU (Ireland)"},
			},
		},
	}, nil
}

func (p *ModalProvider) CheckInstanceTypeAvailability(httpClient *http.Client, apiToken string, instanceTypeName string) ([]Region, error) {
	instances, err := p.GetAvailableInstanceTypes(httpClient, apiToken)
	if err != nil {
		return nil, err
	}

	for _, inst := range instances {
		if inst.InstanceType.Name == instanceTypeName {
			return inst.RegionsWithCapacityAvailable, nil
		}
	}

	return nil, fmt.Errorf("instance type %s not found", instanceTypeName)
}

func (p *ModalProvider) CreateFilesystem(httpClient *http.Client, apiToken string, name string, region string) (*Filesystem, error) {
	client, err := p.getClient()
	if err != nil {
		return nil, err
	}

	ctx := context.Background()

	vol, err := client.Volumes.FromName(ctx, name, &modal.VolumeFromNameParams{
		CreateIfMissing: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Modal Volume: %w", err)
	}

	database, dbErr := db.InitDB()
	if dbErr == nil {
		defer database.Close()
		database.SaveFilesystem(&db.Filesystem{
			Name:   name,
			ID:     vol.VolumeID,
			Region: region,
		})
	}

	return &Filesystem{
		ID:   vol.VolumeID,
		Name: name,
		Region: Region{
			Name: region,
		},
	}, nil
}

func (p *ModalProvider) GetFilesystemInfo(filesystemName string) (*FilesystemInfo, error) {
	db, err := db.InitDB()
	if err != nil {
		return nil, fmt.Errorf("failed to init db: %w", err)
	}
	defer db.Close()

	fs, err := db.GetFilesystem(filesystemName)
	if err != nil {
		return nil, fmt.Errorf("filesystem %s not found: %w", filesystemName, err)
	}

	return &FilesystemInfo{
		ID:     fs.ID,
		Region: fs.Region,
	}, nil
}

func (p *ModalProvider) ListFilesystems(httpClient *http.Client, apiToken string) ([]Filesystem, error) {
	return nil, fmt.Errorf("ListFilesystems not yet implemented for Modal provider")
}

func (p *ModalProvider) DeleteFilesystem(httpClient *http.Client, apiToken string, filesystemID string) error {
	client, err := p.getClient()
	if err != nil {
		return err
	}

	ctx := context.Background()

	err = client.Volumes.Delete(ctx, filesystemID, &modal.VolumeDeleteParams{})
	if err != nil {
		return fmt.Errorf("failed to delete Modal Volume: %w", err)
	}

	database, dbErr := db.InitDB()
	if dbErr == nil {
		defer database.Close()
		database.DeleteFilesystem(filesystemID)
	}

	return nil
}

func (p *ModalProvider) GetGlobalFirewallRules(httpClient *http.Client, apiToken string) (*GlobalFirewallRuleset, error) {
	return nil, fmt.Errorf("firewall rules are not applicable for Modal provider - Modal handles networking automatically")
}

func (p *ModalProvider) UpdateGlobalFirewallRules(httpClient *http.Client, apiToken string, rules []FirewallRule) (*GlobalFirewallRuleset, error) {
	return nil, fmt.Errorf("firewall rules are not applicable for Modal provider")
}

func (p *ModalProvider) EnsurePortOpen(httpClient *http.Client, apiToken string, port int, protocol string, description string) error {
	return nil
}

func (p *ModalProvider) ExecuteBashCommand(instanceID string, command string) (string, error) {
	client, err := p.getClient()
	if err != nil {
		return "", err
	}

	ctx := context.Background()

	sandbox, err := client.Sandboxes.FromID(ctx, instanceID)
	if err != nil {
		return "", fmt.Errorf("failed to get Modal Sandbox: %w", err)
	}

	proc, err := sandbox.Exec(ctx, []string{"/bin/bash", "-c", command}, &modal.SandboxExecParams{
		Stdout: modal.Pipe,
		Stderr: modal.Pipe,
	})
	if err != nil {
		return "", fmt.Errorf("failed to execute command: %w", err)
	}

	// Read stdout
	output, err := io.ReadAll(proc.Stdout)
	if err != nil {
		return "", fmt.Errorf("failed to read stdout: %w", err)
	}

	// Read stderr
	stderr, err := io.ReadAll(proc.Stderr)
	if err != nil {
		return "", fmt.Errorf("failed to read stderr: %w", err)
	}

	waitCode, err := proc.Wait(ctx)
	if err != nil {
		return "", fmt.Errorf("command failed: %w", err)
	}

	if waitCode != 0 {
		return "", fmt.Errorf("command exited with code %d: %s", waitCode, string(stderr))
	}

	return string(output), nil
}

func (p *ModalProvider) ExecutePythonCode(instanceID string, code string, timeout int) (string, error) {
	return p.ExecuteBashCommand(instanceID, fmt.Sprintf("python3 -c '%s'", code))
}
