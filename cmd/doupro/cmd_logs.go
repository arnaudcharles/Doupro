package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/arnaudcharles/doupro/internal/cliclient"
)

func logsCmd() *cobra.Command {
	var (
		event, container, level string
		follow                  bool
		limit                   int
	)
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Show or follow the event log",
		RunE: func(cmd *cobra.Command, args []string) error {
			filter := cliclient.LogFilter{Event: event, Container: container, Level: level, Limit: limit}
			c := client()

			if !follow {
				logs, err := c.ListLogs(cmd.Context(), filter)
				if err != nil {
					return err
				}
				if flagJSON {
					return printJSON(logs)
				}
				for _, l := range reversed(logs) {
					printLogLine(l)
				}
				return nil
			}

			return followLogs(cmd.Context(), c, filter)
		},
	}
	cmd.Flags().StringVar(&event, "event", "", "filter by event type, e.g. update.failed")
	cmd.Flags().StringVar(&container, "container", "", "filter by container name")
	cmd.Flags().StringVar(&level, "level", "", "filter by level: debug|info|warn|error")
	cmd.Flags().BoolVar(&follow, "follow", false, "poll for new events and print them as they arrive")
	cmd.Flags().IntVar(&limit, "limit", 300, "max events to show (non-follow mode; server enforces 50-1000)")
	return cmd
}

// followLogs polls GET /api/v1/logs — there's no streaming/SSE endpoint
// yet (docs/api.md describes one for GET /api/v1/logs/stream), so
// --follow is implemented as a short poll loop instead.
func followLogs(ctx context.Context, c *cliclient.Client, filter cliclient.LogFilter) error {
	var lastID int64
	first := true
	for {
		filter.Limit = 100
		logs, err := c.ListLogs(ctx, filter)
		if err != nil {
			return err
		}
		fresh := reversed(logs)
		for _, l := range fresh {
			if l.ID <= lastID {
				continue
			}
			if !first {
				printLogLine(l)
			}
			lastID = l.ID
		}
		first = false

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

func reversed(logs []cliclient.LogEntry) []cliclient.LogEntry {
	out := make([]cliclient.LogEntry, len(logs))
	for i, l := range logs {
		out[len(logs)-1-i] = l
	}
	return out
}

func printLogLine(l cliclient.LogEntry) {
	container := ""
	if l.Container != "" {
		container = " " + l.Container
	}
	fmt.Fprintf(os.Stdout, "%s %-5s %-24s%s %s\n", l.Timestamp, l.Level, l.Event, container, l.Message) //nolint:errcheck // stdout write, nothing to recover
}
