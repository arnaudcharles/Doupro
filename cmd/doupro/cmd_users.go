package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/arnaudcharles/doupro/internal/cliclient"
)

func accessFlags(cmd *cobra.Command, access *cliclient.Access) {
	cmd.Flags().StringVar(&access.Role, "role", "read", "role: admin, write, or read")
	cmd.Flags().BoolVar(&access.ViewLogs, "view-logs", false, "allow viewing logs")
	cmd.Flags().BoolVar(&access.ManageSchedules, "manage-schedules", false, "allow managing schedules")
	cmd.Flags().BoolVar(&access.ManageNotifications, "manage-notifications", false, "allow managing notifications")
	cmd.Flags().BoolVar(&access.ViewStats, "view-stats", false, "allow viewing stats")
}

func usersCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "users", Short: "Manage users and RBAC assignments"}
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		users, err := client().ListUsers(cmd.Context())
		if err != nil {
			return err
		}
		if flagJSON {
			return printJSON(users)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tUSERNAME\tPROVIDER\tROLE\tLOGS\tSCHEDULES\tNOTIFICATIONS\tSTATS") //nolint:errcheck // stdout write, nothing to recover
		for _, u := range users {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%t\t%t\t%t\t%t\n", u.ID, u.Username, u.AuthProvider, u.Access.Role, u.Access.ViewLogs, u.Access.ManageSchedules, u.Access.ManageNotifications, u.Access.ViewStats) //nolint:errcheck // stdout write, nothing to recover
		}
		return w.Flush()
	}}
	var password string
	var createAccess cliclient.Access
	create := &cobra.Command{Use: "create <username>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if password == "" {
			return fmt.Errorf("--password is required")
		}
		u, err := client().CreateUser(cmd.Context(), args[0], password, createAccess)
		if err != nil {
			return err
		}
		if flagJSON {
			return printJSON(u)
		}
		fmt.Printf("user %s created with role %s\n", u.Username, u.Access.Role)
		return nil
	}}
	create.Flags().StringVar(&password, "password", "", "initial password (minimum 8 characters)")
	accessFlags(create, &createAccess)
	var updateAccess cliclient.Access
	setAccess := &cobra.Command{Use: "set-access <user-id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := client().SetUserAccess(cmd.Context(), args[0], updateAccess); err != nil {
			return err
		}
		fmt.Println("user access updated")
		return nil
	}}
	accessFlags(setAccess, &updateAccess)
	deleteCmd := &cobra.Command{Use: "delete <user-id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := client().DeleteUser(cmd.Context(), args[0]); err != nil {
			return err
		}
		fmt.Println("user deleted")
		return nil
	}}
	cmd.AddCommand(list, create, setAccess, deleteCmd)
	return cmd
}

func apiKeysCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "api-keys", Short: "Manage role-scoped API keys"}
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		keys, err := client().ListAPIKeys(cmd.Context())
		if err != nil {
			return err
		}
		if flagJSON {
			return printJSON(keys)
		}
		for _, k := range keys {
			fmt.Printf("%d\t%s\t%s…\t%s\n", k.ID, k.Name, k.KeyPrefix, k.Access.Role)
		}
		return nil
	}}
	var access cliclient.Access
	create := &cobra.Command{Use: "create <name>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		key, err := client().CreateAPIKey(cmd.Context(), args[0], access)
		if err != nil {
			return err
		}
		if flagJSON {
			return printJSON(key)
		}
		fmt.Printf("New key (shown once): %s\n", key.Key)
		return nil
	}}
	accessFlags(create, &access)
	revoke := &cobra.Command{Use: "revoke <key-id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := client().RevokeAPIKey(cmd.Context(), args[0]); err != nil {
			return err
		}
		fmt.Println("API key revoked")
		return nil
	}}
	cmd.AddCommand(list, create, revoke)
	return cmd
}
