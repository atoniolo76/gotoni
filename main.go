package main

import (
	"context"
	"log"
	"os"

	"github.com/urfave/cli/v3"
	"toni/gpusnapshot/cmd/cluster"
)

func main() {
	cmd := &cli.Command{
		Name:  "gpusnapshot",
		Usage: "GPU cloud instance management tool",
		Description: "A tool for managing GPU cloud instances across different providers",
		Commands: []*cli.Command{
			cluster.NewCommand(),
			// Add more commands here as you implement them
		},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		log.Fatal(err)
	}
}