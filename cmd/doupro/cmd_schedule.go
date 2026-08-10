package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/arnaudcharles/doupro/internal/cliclient"
)

func scheduleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "Manage one-off and recurring update schedules",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List schedules",
		RunE: func(cmd *cobra.Command, args []string) error {
			schedules, err := client().ListSchedules(cmd.Context())
			if err != nil {
				return err
			}
			if flagJSON {
				return printJSON(schedules)
			}
			printSchedulesTable(schedules)
			return nil
		},
	}

	var (
		container, stack, at, cronExpr, policy, after, semverPolicy string
	)
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a schedule",
		Long: `Create a one-off, cron, or relative schedule.

  doupro schedule create --container adguard-home --at 2026-08-02T19:00:00Z
  doupro schedule create --stack media --at 2026-08-02T19:00:00Z
  doupro schedule create --stack media --cron "0 8 * * MON"
  doupro schedule create --stack media --policy delayed --after 24h`,
		RunE: func(cmd *cobra.Command, args []string) error {
			req := cliclient.CreateScheduleRequest{Container: container, Stack: stack, Semver: semverPolicy}
			switch {
			case at != "":
				req.Type, req.RunAt = "once", at
			case cronExpr != "":
				req.Type, req.Cron = "cron", cronExpr
			case policy != "":
				req.Type, req.Policy, req.After = "relative", policy, after
			default:
				return fmt.Errorf("specify one of --at, --cron, or --policy")
			}

			schedules, err := client().CreateSchedule(cmd.Context(), req)
			if err != nil {
				return err
			}
			if len(schedules) == 1 {
				fmt.Printf("schedule #%d created\n", schedules[0].ID)
				return nil
			}
			fmt.Printf("%d schedules created\n", len(schedules))
			return nil
		},
	}
	create.Flags().StringVar(&container, "container", "", "target container name (for --at)")
	create.Flags().StringVar(&stack, "stack", "", "target stack (for --cron or --policy)")
	create.Flags().StringVar(&at, "at", "", "run once at this RFC3339 timestamp")
	create.Flags().StringVar(&cronExpr, "cron", "", "recurring cron expression")
	create.Flags().StringVar(&policy, "policy", "", `relative recurring policy: "immediate" or "delayed"`)
	create.Flags().StringVar(&after, "after", "", `delay for --policy delayed, e.g. "24h"`)
	create.Flags().StringVar(&semverPolicy, "semver", "patch", `allowed version scope: "patch", "minor", or "major"`)

	cancel := &cobra.Command{
		Use:   "cancel <id>",
		Short: "Cancel a schedule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client().DeleteSchedule(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Printf("schedule #%s cancelled\n", args[0])
			return nil
		},
	}

	enable := &cobra.Command{
		Use:   "enable <id>",
		Short: "Re-enable a disabled schedule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client().SetScheduleEnabled(cmd.Context(), args[0], true); err != nil {
				return err
			}
			fmt.Printf("schedule #%s enabled\n", args[0])
			return nil
		},
	}

	disable := &cobra.Command{
		Use:   "disable <id>",
		Short: "Disable a schedule without deleting it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client().SetScheduleEnabled(cmd.Context(), args[0], false); err != nil {
				return err
			}
			fmt.Printf("schedule #%s disabled\n", args[0])
			return nil
		},
	}

	cmd.AddCommand(list, create, cancel, enable, disable)
	return cmd
}

func printSchedulesTable(schedules []cliclient.Schedule) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTYPE\tTARGET\tWHEN\tENABLED") //nolint:errcheck // stdout write, nothing to recover
	for _, sc := range schedules {
		target := sc.Container
		if target == "" {
			target = sc.Stack
		}
		when := sc.RunAt
		if sc.Cron != "" {
			when = sc.Cron
		} else if sc.Policy != "" {
			when = sc.Policy
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s (%s)\t%v\n", sc.ID, sc.Type, target, when, sc.Semver, sc.Enabled) //nolint:errcheck // stdout write, nothing to recover
	}
	tw.Flush() //nolint:errcheck // stdout write, nothing to recover
}
