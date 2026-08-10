package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func statsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "Show a stats summary",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := client().Stats(cmd.Context())
			if err != nil {
				return err
			}
			if flagJSON {
				return printJSON(s)
			}
			fmt.Printf("Containers:     %d tracked, %d running, %d update(s) available\n",
				s.ContainersTotal, s.ContainersRunning, s.UpdatesAvailable)
			fmt.Printf("Updates:        %d succeeded, %d failed (recent)\n", s.UpdatesSucceeded, s.UpdatesFailed)
			fmt.Printf("Rollbacks:      %d auto, %d manual (recent)\n", s.RollbacksAuto, s.RollbacksManual)
			fmt.Printf("Notifications:  %d sent, %d failed (recent)\n", s.NotificationsSent, s.NotificationsFailed)
			fmt.Printf("Schedules:      %d active\n", s.SchedulesActive)
			return nil
		},
	}
}
