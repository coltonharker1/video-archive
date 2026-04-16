package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show job queue status",
	RunE:  runStatus,
}

func init() {
	RootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, args []string) error {
	cfg := getConfig()
	db, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	counts, err := db.CountJobs()
	if err != nil {
		return err
	}

	total := counts.Pending + counts.Running + counts.Complete + counts.Failed
	if total == 0 {
		fmt.Println("No jobs in queue.")
		return nil
	}

	fmt.Println("Job Queue Status")
	fmt.Println("-----------------")
	fmt.Printf("Pending:   %d\n", counts.Pending)
	fmt.Printf("Running:   %d\n", counts.Running)
	fmt.Printf("Complete:  %d\n", counts.Complete)
	fmt.Printf("Failed:    %d\n", counts.Failed)
	fmt.Printf("Total:     %d\n", total)

	// Show recent jobs
	jobs, err := db.ListRecentJobs(10)
	if err != nil {
		return err
	}
	if len(jobs) > 0 {
		fmt.Printf("\nRecent Jobs\n")
		fmt.Printf("%-4s  %-4s  %-10s  %-10s  %s\n", "ID", "Rec", "Type", "Status", "Error")
		fmt.Println("----  ----  ----------  ----------  -----")
		for _, j := range jobs {
			errMsg := j.Error
			if len(errMsg) > 40 {
				errMsg = errMsg[:37] + "..."
			}
			fmt.Printf("%-4d  %-4d  %-10s  %-10s  %s\n",
				j.ID, j.RecordingID, j.Type, j.Status, errMsg)
		}
	}

	return nil
}
