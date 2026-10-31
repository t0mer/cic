package main

import (
	"errors"

	"github.com/spf13/cobra"
)

var errNotImplemented = errors.New("not implemented")

func newUserCmd() *cobra.Command {
	user := &cobra.Command{Use: "user", Short: "Manage local users"}

	add := &cobra.Command{
		Use:   "add <username>",
		Short: "Add a local user",
		Args:  cobra.ExactArgs(1),
		RunE:  func(*cobra.Command, []string) error { return errNotImplemented },
	}
	add.Flags().Bool("admin", false, "grant admin rights")

	reset := &cobra.Command{
		Use:   "reset-password <username>",
		Short: "Reset a local user's password",
		Args:  cobra.ExactArgs(1),
		RunE:  func(*cobra.Command, []string) error { return errNotImplemented },
	}

	user.AddCommand(add, reset)
	return user
}
