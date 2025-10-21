package cli

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/urfave/cli/v3"
	"toni/gpusnapshot/pkg/providers"
)

// NewClusterCommand creates and returns the cluster command
func NewClusterCommand() *cli.Command {
	return &cli.Command{
		Name:  "cluster",
		Usage: "Manage GPU clusters",
		Description: "Commands for managing GPU cloud instances and clusters",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			// Default action when no subcommand is specified
			return cmd.Command("help").Action(ctx, cmd.Command("help"))
		},
		Commands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List GPU instances",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					log.Println("Listing GPU instances...")
					// TODO: Implement instance listing
					return nil
				},
			},
			{
				Name:  "launch",
				Usage: "Launch a new GPU instance",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:     "type",
						Usage:    "Instance type to launch",
						Required: true,
					},
					&cli.StringFlag{
						Name:  "region",
						Usage: "Region to launch in",
						Value: "us-west-2",
					},
					&cli.StringFlag{
						Name:  "name",
						Usage: "Name for the instance",
					},
					&cli.BoolFlag{
						Name:  "k3s",
						Usage: "Install k3s (lightweight Kubernetes) on the instance",
					},
					&cli.StringFlag{
						Name:  "k3s-version",
						Usage: "k3s version to install (default: latest)",
						Value: "latest",
					},
					&cli.BoolFlag{
						Name:  "k3s-server",
						Usage: "Run k3s as server (control plane)",
						Value: true,
					},
					&cli.StringSliceFlag{
						Name:  "k3s-args",
						Usage: "Additional k3s arguments",
					},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					instanceType := cmd.String("type")
					region := cmd.String("region")
					name := cmd.String("name")
					installK3s := cmd.Bool("k3s")
					k3sVersion := cmd.String("k3s-version")
					k3sServer := cmd.Bool("k3s-server")
					k3sArgs := cmd.StringSlice("k3s-args")

					log.Printf("Launching instance: type=%s, region=%s, name=%s, k3s=%v",
						instanceType, region, name, installK3s)

					// Build container configuration
					var container *providers.ContainerConfig
					if installK3s {
						container = buildK3sContainer(k3sVersion, k3sServer, k3sArgs)
						log.Printf("Will install k3s %s (server: %v)", k3sVersion, k3sServer)
						log.Printf("Container config: image=%s, command=%v, args=%v",
							container.Image, container.Command, container.Args)
					}

					// TODO: Implement actual instance launching with provider
					// This would call your provider.LaunchInstance() with the container config

					return nil
				},
			},
		},
	}
}

// buildK3sContainer creates a container configuration for k3s
func buildK3sContainer(version string, isServer bool, extraArgs []string) *providers.ContainerConfig {
	image := "rancher/k3s"
	if version != "latest" {
		// Remove 'v' prefix if present to avoid double 'v'
		cleanVersion := strings.TrimPrefix(version, "v")
		image = fmt.Sprintf("%s:v%s", image, cleanVersion)
	}

	// Build k3s command
	cmd := []string{}
	args := []string{}

	if isServer {
		cmd = append(cmd, "server")
	} else {
		cmd = append(cmd, "agent")
	}

	// Add extra arguments
	if len(extraArgs) > 0 {
		args = append(args, extraArgs...)
	}

	// Add common k3s flags for GPU/container workloads
	args = append(args, "--docker") // Use Docker instead of containerd for compatibility
	args = append(args, "--kubelet-arg=feature-gates=KubeletInUserNamespace=true")

	// Expose Kubernetes API port
	ports := []string{"6443/tcp"}
	if isServer {
		ports = append(ports, "8080/tcp", "10250/tcp") // Additional server ports
	}

	return &providers.ContainerConfig{
		Image:   image,
		Ports:   ports,
		Command: cmd,
		Args:    args,
	}
}
