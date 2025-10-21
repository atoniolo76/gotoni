package cli

import (
	"context"
	"log"

	"github.com/urfave/cli/v3"
	"toni/gpusnapshot/pkg/cluster"
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

					// Get global provider from context/flags
					provider := "lambdalabs" // TODO: Get from global --provider flag

					log.Printf("Launching instance: type=%s, region=%s, name=%s, k3s=%v",
						instanceType, region, name, installK3s)

					// Create cluster manager
					manager := cluster.NewManager()

					// Build k3s config if enabled
					var k3sConfig *cluster.K3sConfig
					if installK3s {
						k3sConfig = cluster.NewK3sConfig(true, k3sVersion, k3sServer, k3sArgs)
						log.Printf("Will install k3s %s (server: %v)", k3sVersion, k3sServer)

						// Show container config for debugging
						container := manager.BuildK3sContainer(k3sVersion, k3sServer, k3sArgs)
						log.Printf("Container config: image=%s, command=%v, args=%v",
							container.Image, container.Command, container.Args)
					}

					// Launch instance using manager
					instance, err := manager.LaunchInstance(provider, instanceType, region, name, k3sConfig)
					if err != nil {
						log.Printf("Failed to launch instance: %v", err)
						return err
					}

					if instance != nil {
						log.Printf("Successfully launched instance: %s (ID: %s)", instance.Name, instance.ID)
					} else {
						log.Printf("Instance launch initiated (async)")
					}

					return nil
				},
			},
		},
	}
}

