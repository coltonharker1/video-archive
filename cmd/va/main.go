package main

import (
	"os"

	"github.com/colton/video-archive/internal/cli"
)

func main() {
	if err := cli.RootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
