package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/arnaudcharles/doupro/internal/cliclient"
)

func notificationsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notifications",
		Short: "Show the notification delivery log",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List recent notification delivery attempts",
		RunE: func(cmd *cobra.Command, args []string) error {
			notifications, err := client().ListNotifications(cmd.Context())
			if err != nil {
				return err
			}
			if flagJSON {
				return printJSON(notifications)
			}
			printNotificationsTable(notifications)
			return nil
		},
	}
	queue := &cobra.Command{
		Use:   "queue",
		Short: "List durable notification jobs",
		RunE: func(cmd *cobra.Command, args []string) error {
			jobs, err := client().ListNotificationQueue(cmd.Context())
			if err != nil {
				return err
			}
			if flagJSON {
				return printJSON(jobs)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tSTATUS\tATTEMPTS\tNEXT ATTEMPT\tCHANNEL\tEVENT") //nolint:errcheck // stdout write, nothing to recover
			for _, job := range jobs {
				fmt.Fprintf(tw, "%d\t%s\t%d/%d\t%s\t%s\t%s\n", job.ID, job.Status, job.Attempts, job.MaxAttempts, job.NextAttemptAt, job.Channel, job.Event) //nolint:errcheck // stdout write, nothing to recover
			}
			return tw.Flush()
		},
	}
	retry := &cobra.Command{
		Use: "retry <queue-id>", Short: "Retry a dead-letter notification", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return client().RetryNotification(cmd.Context(), args[0])
		},
	}
	channels := &cobra.Command{
		Use: "channels", Short: "List configured channels without revealing secrets",
		RunE: func(cmd *cobra.Command, args []string) error {
			items, err := client().ListNotificationChannels(cmd.Context())
			if err != nil {
				return err
			}
			if flagJSON {
				return printJSON(items)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tPROVIDER\tCONFIGURED") //nolint:errcheck // stdout write, nothing to recover
			for _, item := range items {
				fmt.Fprintf(tw, "%s\t%s\t%t\n", item.Name, item.Provider, item.Configured) //nolint:errcheck // stdout write, nothing to recover
			}
			return tw.Flush()
		},
	}

	cmd.AddCommand(list, queue, retry, channels)
	return cmd
}

func printNotificationsTable(notifications []cliclient.Notification) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tSTATUS\tEVENT\tCHANNEL\tMESSAGE") //nolint:errcheck // stdout write, nothing to recover
	for _, n := range notifications {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", n.Timestamp, n.Status, n.Event, n.Channel, n.Message) //nolint:errcheck // stdout write, nothing to recover
	}
	tw.Flush() //nolint:errcheck // stdout write, nothing to recover
}
