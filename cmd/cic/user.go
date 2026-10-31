package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/t0mer/cic/internal/auth"
	"github.com/t0mer/cic/internal/store"
)

// termReader reads a password without echo. Replaced in tests.
type termReader func(prompt string) (string, error)

func readTerminal(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("stdin is not a terminal; use --password-stdin")
	}
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	return string(b), err
}

// readPassword obtains a password from stdin (one line) or by prompting twice.
func readPassword(stdin io.Reader, fromStdin bool, prompt termReader) (string, error) {
	if fromStdin {
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && (err != io.EOF || line == "") {
			return "", errors.New("reading password from stdin: no input")
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	pw, err := prompt("Password: ")
	if err != nil {
		return "", err
	}
	again, err := prompt("Repeat password: ")
	if err != nil {
		return "", err
	}
	if pw != again {
		return "", errors.New("passwords do not match")
	}
	return pw, nil
}

// userDeps are the injectable inputs of the user commands.
type userDeps struct {
	prompt termReader
	open   func(cmd *cobra.Command) (*sql.DB, error)
}

func openUserDB(cmd *cobra.Command) (*sql.DB, error) {
	_ = godotenv.Load()
	cfg, err := loadConfig(cmd)
	if err != nil {
		return nil, err
	}
	db, err := store.Open(cmd.Context(), cfg.Storage.Path)
	if err != nil {
		return nil, fmt.Errorf("opening store: %w", err)
	}
	return db, nil
}

func newUserCmd() *cobra.Command {
	return newUserCmdWith(userDeps{prompt: readTerminal, open: openUserDB})
}

func newUserCmdWith(d userDeps) *cobra.Command {
	user := &cobra.Command{Use: "user", Short: "Manage local users"}
	user.PersistentFlags().String("db-path", "/data/cic.db", "SQLite database path (env CIC_STORAGE_PATH)")

	// with opens the database and hands the user store to fn.
	with := func(fn func(cmd *cobra.Command, st *auth.Store, args []string) error) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			if cmd.Context() == nil {
				cmd.SetContext(context.Background())
			}
			db, err := d.open(cmd)
			if err != nil {
				return err
			}
			defer db.Close()
			return fn(cmd, auth.NewStore(db, nil), args)
		}
	}

	add := &cobra.Command{
		Use:   "add <username>",
		Short: "Add a local user",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(cmd *cobra.Command, st *auth.Store, args []string) error {
			admin, _ := cmd.Flags().GetBool("admin")
			stdin, _ := cmd.Flags().GetBool("password-stdin")
			pw, err := readPassword(cmd.InOrStdin(), stdin, d.prompt)
			if err != nil {
				return err
			}
			role := auth.RoleViewer
			if admin {
				role = auth.RoleAdmin
			}
			if _, err := st.CreateUser(cmd.Context(), args[0], pw, role, false); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "user %s created (role %s)\n", args[0], role)
			return nil
		}),
	}
	add.Flags().Bool("admin", false, "grant admin rights (default: read-only viewer)")
	add.Flags().Bool("password-stdin", false, "read the password from stdin instead of prompting")

	reset := &cobra.Command{
		Use:   "reset-password <username>",
		Short: "Reset a local user's password (forces a change at next login, ends sessions)",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(cmd *cobra.Command, st *auth.Store, args []string) error {
			stdin, _ := cmd.Flags().GetBool("password-stdin")
			u, _, err := st.UserByName(cmd.Context(), args[0])
			if err != nil {
				return fmt.Errorf("user %q: %w", args[0], err)
			}
			pw, err := readPassword(cmd.InOrStdin(), stdin, d.prompt)
			if err != nil {
				return err
			}
			if err := st.SetPassword(cmd.Context(), u.ID, u.Username, pw, true); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "password for %s reset; a change is required at next login\n", u.Username)
			return nil
		}),
	}
	reset.Flags().Bool("password-stdin", false, "read the password from stdin instead of prompting")

	list := &cobra.Command{
		Use:   "list",
		Short: "List local users",
		Args:  cobra.NoArgs,
		RunE: with(func(cmd *cobra.Command, st *auth.Store, _ []string) error {
			users, err := st.ListUsers(cmd.Context())
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "USERNAME\tROLE\tDISABLED\tLAST LOGIN")
			for _, u := range users {
				last := "never"
				if u.LastLogin != nil {
					last = u.LastLogin.Format("2006-01-02 15:04:05 MST")
				}
				fmt.Fprintf(tw, "%s\t%s\t%v\t%s\n", u.Username, u.Role, u.Disabled, last)
			}
			return tw.Flush()
		}),
	}

	setDisabled := func(use, short string, disabled bool) *cobra.Command {
		return &cobra.Command{
			Use:   use + " <username>",
			Short: short,
			Args:  cobra.ExactArgs(1),
			RunE: with(func(cmd *cobra.Command, st *auth.Store, args []string) error {
				u, _, err := st.UserByName(cmd.Context(), args[0])
				if err != nil {
					return fmt.Errorf("user %q: %w", args[0], err)
				}
				if err := st.SetDisabled(cmd.Context(), u.ID, disabled); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "user %s %sd\n", u.Username, use)
				return nil
			}),
		}
	}

	user.AddCommand(add, reset, list,
		setDisabled("disable", "Disable a user and end their sessions", true),
		setDisabled("enable", "Re-enable a user", false))
	return user
}
