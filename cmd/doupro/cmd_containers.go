package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/arnaudcharles/doupro/internal/cliclient"
)

func containersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "containers",
		Short: "List and inspect tracked containers",
	}

	var stack string
	list := &cobra.Command{
		Use:   "list",
		Short: "List every tracked container",
		RunE: func(cmd *cobra.Command, args []string) error {
			containers, err := client().ListContainers(cmd.Context(), stack)
			if err != nil {
				return err
			}
			if flagJSON {
				return printJSON(containers)
			}
			printContainersTable(containers)
			return nil
		},
	}
	list.Flags().StringVar(&stack, "stack", "", "filter to one stack")

	inspect := &cobra.Command{
		Use:   "inspect <name>",
		Short: "Show full detail for one container",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client().GetContainer(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return printJSON(c)
		},
	}

	check := &cobra.Command{
		Use:   "check <name>",
		Short: "Force an immediate registry check for one container",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := client().CheckContainer(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if flagJSON {
				return printJSON(result)
			}
			if !result.Checked {
				fmt.Printf("%s: nothing to compare (no recorded local digest)\n", args[0])
				return nil
			}
			fmt.Printf("%s: checked, update available: %t\n", args[0], result.UpdateAvailable)
			return nil
		},
	}

	cmd.AddCommand(list, inspect, check)
	return cmd
}

func updateCmd() *cobra.Command {
	var semverPolicy string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "update <name>",
		Short: "Update a container now (keeps the previous version for rollback)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if dryRun {
				preview, err := client().PreviewUpdate(cmd.Context(), args[0], semverPolicy)
				if err != nil {
					return err
				}
				if flagJSON {
					return printJSON(preview)
				}
				printUpdatePreview(args[0], preview)
				return nil
			}
			if err := client().UpdateContainer(cmd.Context(), args[0], semverPolicy); err != nil {
				return err
			}
			fmt.Printf("%s: update started\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&semverPolicy, "semver", "major", "allowed version scope: patch, minor, or major")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without actually updating")
	return cmd
}

func updateAllCmd() *cobra.Command {
	var stack string
	cmd := &cobra.Command{
		Use:   "update-all",
		Short: "Update every container that currently has an update available",
		Long: "Update every container that currently has an update available, skipping\n" +
			"excluded containers and DoUpRo's own container. Pass --stack to scope it\n" +
			"to one stack instead of the whole fleet. Updates run in the background;\n" +
			"this only reports how many were queued.",
		RunE: func(cmd *cobra.Command, args []string) error {
			queued, err := client().UpdateAllContainers(cmd.Context(), stack)
			if err != nil {
				return err
			}
			fmt.Printf("%d container(s) queued for update\n", queued)
			return nil
		},
	}
	cmd.Flags().StringVar(&stack, "stack", "", "limit to one stack")
	return cmd
}

func rollbackCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rollback <name>",
		Short: "Roll back a container to its recorded previous version",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client().RollbackContainer(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Printf("%s: rollback started\n", args[0])
			return nil
		},
	}
}

func printUpdatePreview(name string, p cliclient.UpdatePreview) {
	if !p.UpdateAvailable {
		fmt.Printf("%s: already up to date (%s)\n", name, p.CurrentImage)
		return
	}
	kind := "update"
	if p.UpdateKind == "rebuild" {
		kind = "image rebuild (same resolved version, new digest)"
	}
	from, to := p.CurrentImage, p.TargetImage
	if p.CurrentVersion != "" {
		from = p.CurrentVersion
	}
	if p.TargetVersion != "" {
		to = p.TargetVersion
	}
	fmt.Printf("%s: %s available (semver scope: %s)\n  %s -> %s\n", name, kind, p.SemverScope, from, to)
}

func printContainersTable(containers []cliclient.Container) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTACK\tSTATE\tCURRENT\tUPDATE?") //nolint:errcheck // stdout write, nothing to recover
	for _, c := range containers {
		stack := c.Stack
		if stack == "" {
			stack = "-"
		}
		update := ""
		if c.UpdateAvailable {
			update = "yes"
		}
		state := c.State
		if c.Operation != nil {
			state = c.Operation.Kind + " in progress"
		} else if c.RollbackWatch != nil && c.RollbackWatch.CrashCount > 0 {
			state = fmt.Sprintf("%s (crash guard %d/%d)", state, c.RollbackWatch.CrashCount, c.RollbackWatch.Threshold)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", c.Name, stack, state, c.CurrentImage, update) //nolint:errcheck // stdout write, nothing to recover
	}
	tw.Flush() //nolint:errcheck // stdout write, nothing to recover
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
