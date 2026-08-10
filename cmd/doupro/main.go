// Command doupro is both the DoUpRo daemon and its own CLI client:
// invoked as `doupro serve` it runs the daemon (the container's default
// CMD); every other subcommand is a thin REST API client (see
// docs/cli.md) built on internal/cliclient — none of them contain
// business logic of their own, only argument parsing and HTTP calls.
package main

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/arnaudcharles/doupro/internal/cliclient"
)

var version = "dev"

var (
	flagHost   string
	flagAPIKey string
	flagJSON   bool
)

func main() {
	root := &cobra.Command{
		Use:           "doupro",
		Short:         "DoUpRo - Docker Update Rollback",
		SilenceUsage:  true,
		SilenceErrors: false,
	}

	root.PersistentFlags().StringVar(&flagHost, "host", envOr("DOUPRO_HOST", ""),
		"DoUpRo API base URL, e.g. https://doupro.home.arpa (env DOUPRO_HOST)")
	root.PersistentFlags().StringVar(&flagAPIKey, "api-key", envOr("DOUPRO_API_KEY", ""),
		"API key from Settings -> Security (env DOUPRO_API_KEY)")
	root.PersistentFlags().BoolVar(&flagJSON, "json", false, "output machine-readable JSON instead of a table")

	root.AddCommand(
		serveCmd(),
		versionCmd(),
		healthcheckCmd(),
		selfUpdateHelperCmd(),
		containersCmd(),
		updateCmd(),
		updateAllCmd(),
		rollbackCmd(),
		scheduleCmd(),
		logsCmd(),
		notificationsCmd(),
		statsCmd(),
		settingsCmd(),
		usersCmd(),
		apiKeysCmd(),
	)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// client builds the API client shared by every non-serve subcommand, per
// the --host/--api-key persistent flags (or their DOUPRO_HOST/
// DOUPRO_API_KEY env var defaults). When neither is set, it tries the
// daemon's local Unix socket instead (see cliclient.NewLocal) — what makes
// `docker exec doupro doupro ...` work with no flags at all, per
// docs/cli.md. An explicit --host always wins, even with the socket
// present, so a real remote target is never silently overridden.
func client() *cliclient.Client {
	if flagHost == "" && flagAPIKey == "" {
		if socketPath := envOr("DOUPRO_LOCAL_SOCKET", "/data/doupro.sock"); cliclient.LocalSocketReachable(socketPath) {
			return cliclient.NewLocal(socketPath)
		}
	}
	return cliclient.New(flagHost, flagAPIKey)
}
