package main

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/spf13/cobra"
)

func newHealthcheckCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "healthcheck",
		Short: "Probe the local /healthz endpoint (for container HEALTHCHECK)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Only the listen address is needed, so skip full validation.
			cfg, err := loadConfigUnvalidated(cmd)
			if err != nil {
				return err
			}
			addr, err := probeAddr(cfg.Server.Listen)
			if err != nil {
				return err
			}
			client := &http.Client{Timeout: 3 * time.Second}
			resp, err := client.Get("http://" + addr + "/healthz")
			if err != nil {
				return fmt.Errorf("health probe failed: %w", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("health probe returned %s", resp.Status)
			}
			return nil
		},
	}
	cmd.Flags().String("listen", ":8080", "HTTP listen address of the running server (env CIC_SERVER_LISTEN)")
	return cmd
}

// probeAddr derives the host:port to probe from a listen address. Wildcard or
// empty hosts are probed on loopback.
func probeAddr(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("parsing server.listen %q: %w", listen, err)
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port), nil
}
