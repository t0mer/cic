package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/t0mer/cic/internal/version"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), version.Version)
		},
	}
}
