// Command cic is the control plane for DRADIS devices.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/t0mer/cic/internal/config"
	"github.com/t0mer/cic/internal/version"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "cic:", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "cic",
		Short:         "DRADIS control plane",
		Version:       version.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetVersionTemplate("{{.Version}}\n")
	pf := root.PersistentFlags()
	pf.String("config", "", "path to YAML config file (env CIC_CONFIG)")
	pf.String("log-level", "info", "log level: debug, info, warn, error (env CIC_LOG_LEVEL)")
	pf.String("log-format", "text", "log format: text, json (env CIC_LOG_FORMAT)")
	root.AddCommand(newServeCmd(), newHealthcheckCmd(), newVersionCmd(), newUserCmd())
	return root
}

// loadConfigUnvalidated loads configuration using the command's flags, without validation.
func loadConfigUnvalidated(cmd *cobra.Command) (*config.Config, error) {
	path, _ := cmd.Flags().GetString("config")
	if path == "" {
		path = os.Getenv("CIC_CONFIG")
	}
	return config.Load(path, cmd.Flags())
}

// loadConfig loads and validates configuration using the command's flags.
func loadConfig(cmd *cobra.Command) (*config.Config, error) {
	cfg, err := loadConfigUnvalidated(cmd)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}
