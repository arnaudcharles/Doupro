package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/arnaudcharles/doupro/internal/cliclient"
)

// settingsCmd covers only Settings -> General for now — the CLI surface
// for exclusions/security follows the same cliclient pattern once
// there's demand for scripting those instead of using the Settings page.
func settingsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "settings",
		Short: "View or change general settings",
	}

	get := &cobra.Command{
		Use:   "get",
		Short: "Show the current check interval",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := client().GetGeneralSettings(cmd.Context())
			if err != nil {
				return err
			}
			if flagJSON {
				return printJSON(s)
			}
			fmt.Printf("check_interval_minutes: %d\n", s.CheckIntervalMinutes)
			fmt.Printf("crash_loop_threshold: %d\n", s.CrashLoopThreshold)
			fmt.Printf("crash_loop_window_minutes: %d\n", s.CrashLoopWindowMinutes)
			fmt.Printf("logs_page_size: %d\n", s.LogsPageSize)
			return nil
		},
	}

	var minutes int
	set := &cobra.Command{
		Use:   "set-check-interval <minutes>",
		Short: "Set the registry-check interval (minimum 5 minutes)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := fmt.Sscanf(args[0], "%d", &minutes); err != nil {
				return fmt.Errorf("invalid minutes: %s", args[0])
			}
			if err := client().SetGeneralSettings(cmd.Context(), cliclient.GeneralSettings{CheckIntervalMinutes: minutes}); err != nil {
				return err
			}
			fmt.Printf("check interval set to %d minutes\n", minutes)
			return nil
		},
	}

	var threshold, windowMinutes int
	setAutoRollback := &cobra.Command{
		Use:   "set-auto-rollback <crashes> <window-minutes>",
		Short: "Set the post-update crash-loop automatic rollback policy",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := fmt.Sscanf(args[0], "%d", &threshold); err != nil {
				return fmt.Errorf("invalid crash threshold: %s", args[0])
			}
			if _, err := fmt.Sscanf(args[1], "%d", &windowMinutes); err != nil {
				return fmt.Errorf("invalid window minutes: %s", args[1])
			}
			if err := client().SetGeneralSettings(cmd.Context(), cliclient.GeneralSettings{CrashLoopThreshold: threshold, CrashLoopWindowMinutes: windowMinutes}); err != nil {
				return err
			}
			fmt.Printf("automatic rollback set to %d crashes within %d minutes\n", threshold, windowMinutes)
			return nil
		},
	}

	var logsPageSize int
	setLogsPageSize := &cobra.Command{
		Use:   "set-logs-page-size <size>",
		Short: "Set how many recent events the Logs page shows by default (50-1000)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := fmt.Sscanf(args[0], "%d", &logsPageSize); err != nil {
				return fmt.Errorf("invalid size: %s", args[0])
			}
			if err := client().SetGeneralSettings(cmd.Context(), cliclient.GeneralSettings{LogsPageSize: logsPageSize}); err != nil {
				return err
			}
			fmt.Printf("logs page size set to %d\n", logsPageSize)
			return nil
		},
	}

	cmd.AddCommand(get, set, setAutoRollback, setLogsPageSize)
	cmd.AddCommand(registrySettingsCmd())
	return cmd
}

func registrySettingsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "registries", Short: "Manage private registry connectivity"}
	list := &cobra.Command{Use: "list", RunE: func(cmd *cobra.Command, args []string) error {
		configs, err := client().ListRegistryConfigs(cmd.Context())
		if err != nil {
			return err
		}
		return printJSON(configs)
	}}
	var host, username, password, authHost, proxyURL, caFile string
	set := &cobra.Command{Use: "set", RunE: func(cmd *cobra.Command, args []string) error {
		configs, err := client().ListRegistryConfigs(cmd.Context())
		if err != nil {
			return err
		}
		var caPEM string
		if caFile != "" {
			data, readErr := os.ReadFile(caFile)
			if readErr != nil {
				return readErr
			}
			caPEM = string(data)
		}
		next := make([]cliclient.RegistryConfig, 0, len(configs)+1)
		for _, cfg := range configs {
			if cfg.Host != host {
				next = append(next, cfg)
			}
		}
		next = append(next, cliclient.RegistryConfig{Host: host, Username: username, Password: password, AuthHost: authHost, ProxyURL: proxyURL, CACertPEM: caPEM})
		return client().SetRegistryConfigs(cmd.Context(), next)
	}}
	set.Flags().StringVar(&host, "host", "", "registry host[:port] without scheme")
	_ = set.MarkFlagRequired("host")
	set.Flags().StringVar(&username, "username", "", "registry username")
	set.Flags().StringVar(&password, "password", "", "password or pull token (write-only)")
	set.Flags().StringVar(&authHost, "auth-host", "", "trusted token service host when different")
	set.Flags().StringVar(&proxyURL, "proxy", "", "HTTP(S) proxy URL")
	set.Flags().StringVar(&caFile, "ca-file", "", "custom CA PEM file")
	var testHost, testUsername, testPassword, testAuthHost, testProxyURL, testCAFile string
	test := &cobra.Command{Use: "test", RunE: func(cmd *cobra.Command, args []string) error {
		var caPEM string
		if testCAFile != "" {
			data, err := os.ReadFile(testCAFile)
			if err != nil {
				return err
			}
			caPEM = string(data)
		}
		return client().TestRegistryConfig(cmd.Context(), cliclient.RegistryConfig{Host: testHost, Username: testUsername,
			Password: testPassword, AuthHost: testAuthHost, ProxyURL: testProxyURL, CACertPEM: caPEM})
	}}
	test.Flags().StringVar(&testHost, "host", "", "registry host[:port] without scheme")
	_ = test.MarkFlagRequired("host")
	test.Flags().StringVar(&testUsername, "username", "", "registry username (omit to reuse a saved credential)")
	test.Flags().StringVar(&testPassword, "password", "", "password or pull token")
	test.Flags().StringVar(&testAuthHost, "auth-host", "", "trusted token service host when different")
	test.Flags().StringVar(&testProxyURL, "proxy", "", "HTTP(S) proxy URL")
	test.Flags().StringVar(&testCAFile, "ca-file", "", "custom CA PEM file")
	cmd.AddCommand(list, set, test)
	return cmd
}
