package cluster

import (
	"context"
	"log"

	"github.com/urfave/cli/v3"
)

// NewCommand creates and returns the cluster command
func NewCommand() *cli.Command {
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
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					instanceType := cmd.String("type")
					region := cmd.String("region")
					name := cmd.String("name")

					log.Printf("Launching instance: type=%s, region=%s, name=%s", instanceType, region, name)
					// TODO: Implement instance launching
					return nil
				},
			},
		},
	}
}
