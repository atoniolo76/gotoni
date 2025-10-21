package main

import (
	"context"
	"log"
	"os"

	"github.com/urfave/cli/v3"
	gpucli "toni/gpusnapshot/pkg/cli"
)

func main() {
	cmd := &cli.Command{
		Name:  "gpusnapshot",
		Usage: "GPU cloud instance management tool",
		Description: "A tool for managing GPU cloud instances across different providers",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "provider",
				Usage:   "Cloud provider to use",
				Value:   "lambdalabs",
				Aliases: []string{"p"},
			},
			&cli.BoolFlag{
				Name:  "verbose",
				Usage: "Enable verbose output",
				Aliases: []string{"v"},
			},
		},
		Commands: []*cli.Command{
			gpucli.NewClusterCommand(),
		},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		log.Fatal(err)
	}
}