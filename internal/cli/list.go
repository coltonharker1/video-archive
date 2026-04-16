package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all recordings in the archive",
	RunE:  runList,
}

func init() {
	RootCmd.AddCommand(listCmd)
}

func runList(cmd *cobra.Command, args []string) error {
	cfg := getConfig()
	db, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	recs, err := db.ListRecordings()
	if err != nil {
		return err
	}

	if len(recs) == 0 {
		fmt.Println("No recordings in archive.")
		return nil
	}

	fmt.Printf("%-4s  %-12s  %-40s  %s\n", "ID", "Date", "Slug", "Duration")
	fmt.Println("----  ----------  ----------------------------------------  --------")
	for _, r := range recs {
		dur := ""
		if r.DurationMs > 0 {
			sec := r.DurationMs / 1000
			dur = fmt.Sprintf("%d:%02d", sec/60, sec%60)
		}
		label := r.Slug
		if len(label) > 40 {
			label = label[:37] + "..."
		}
		fmt.Printf("%-4d  %-12s  %-40s  %s\n", r.ID, r.Date, label, dur)
	}
	return nil
}
