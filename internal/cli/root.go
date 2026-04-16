package cli

import (
	"github.com/spf13/cobra"
)

var RootCmd = &cobra.Command{
	Use:   "va",
	Short: "Video archive — ingest, analyze, and search people in video footage",
}
