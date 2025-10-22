package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/urfave/cli/v3"
	"toni/gotoni/pkg/lambdalabs"
)

func main() {
	cmd := &cli.Command{
		Name:  "gotoni",
		Usage: "GPU cloud instance management tool",
		Description: "A tool for managing GPU cloud instances with Lambda Labs",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "api-token",
				Usage:    "Lambda Labs API token",
				Required: true,
				Aliases:  []string{"t"},
			},
			&cli.BoolFlag{
				Name:  "verbose",
				Usage: "Enable verbose output",
				Aliases: []string{"v"},
			},
		},
		Commands: []*cli.Command{
			{
				Name:  "instances",
				Usage: "List GPU instances",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					client := lambdalabs.NewClient(cmd.String("api-token"))

					instances, err := client.ListInstances()
					if err != nil {
						return fmt.Errorf("failed to list instances: %w", err)
					}

					fmt.Printf("Found %d instances:\n", len(instances))
					for _, instance := range instances {
						fmt.Printf("- %s (%s): %s in %s\n",
							instance.Name, instance.ID, instance.Status, instance.Region)
					}

					return nil
				},
			},
			{
				Name:  "instance-types",
				Usage: "List available instance types",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					client := lambdalabs.NewClient(cmd.String("api-token"))

					instanceTypes, err := client.ListInstanceTypes()
					if err != nil {
						return fmt.Errorf("failed to list instance types: %w", err)
					}

					fmt.Printf("Available instance types:\n")
					for _, it := range instanceTypes {
						fmt.Printf("- %s: %s (%d GPUs, %d vCPUs, %d GB RAM, $%d/hour)\n",
							it.Name, it.Description, len(it.GPUs), it.VCPUs, it.RAM_GB, it.PriceCentsPerHour)
						if len(it.Regions) > 0 {
							fmt.Printf("  Regions: %v\n", it.Regions)
						}
					}

					return nil
				},
			},
		},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		log.Fatal(err)
	}
}