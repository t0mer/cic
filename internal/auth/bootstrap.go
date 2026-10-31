package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"os"
)

// bootstrapOut receives the one-time generated admin password; tests replace it.
var bootstrapOut io.Writer = os.Stderr

// BootstrapUsername is the account created on first start.
const BootstrapUsername = "admin"

// BootstrapPasswordEnv, when set, supplies the initial admin password.
const BootstrapPasswordEnv = "CIC_ADMIN_PASSWORD"

const (
	// no 0/O/1/l/I: unambiguous when read from a log line
	passwordAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	generatedLength  = 20
)

// GeneratePassword returns a random password from an unambiguous alphabet.
func GeneratePassword() (string, error) {
	out := make([]byte, generatedLength)
	n := big.NewInt(int64(len(passwordAlphabet)))
	for i := range out {
		v, err := rand.Int(rand.Reader, n)
		if err != nil {
			return "", fmt.Errorf("generating password: %w", err)
		}
		out[i] = passwordAlphabet[v.Int64()]
	}
	return string(out), nil
}

// Bootstrap creates the first admin when the user table is empty. With CIC_ADMIN_PASSWORD
// the given password is used (never logged, no forced change); otherwise a random one is
// generated, written once to stderr (not the structured logger) and the account must
// change it on first login.
func Bootstrap(ctx context.Context, st *Store, log *slog.Logger, getenv func(string) string) error {
	n, err := st.CountUsers(ctx)
	if err != nil {
		return fmt.Errorf("counting users: %w", err)
	}
	if n > 0 {
		return nil
	}
	if pw := getenv(BootstrapPasswordEnv); pw != "" {
		if _, err := st.CreateUser(ctx, BootstrapUsername, pw, RoleAdmin, false); err != nil {
			if errors.Is(err, ErrUserExists) {
				return nil
			}
			return fmt.Errorf("creating bootstrap admin from %s: %w", BootstrapPasswordEnv, err)
		}
		log.Info("bootstrap admin created from environment", "username", BootstrapUsername)
		return nil
	}
	pw, err := GeneratePassword()
	if err != nil {
		return err
	}
	if _, err := st.CreateUser(ctx, BootstrapUsername, pw, RoleAdmin, true); err != nil {
		if errors.Is(err, ErrUserExists) {
			return nil
		}
		return fmt.Errorf("creating bootstrap admin: %w", err)
	}
	// The password goes straight to stderr, not through the structured logger,
	// so JSON log shippers and level filters never carry it.
	fmt.Fprintf(bootstrapOut, "cic: initial admin password (shown once, change it at first login): username=%s password=%s\n", BootstrapUsername, pw)
	log.Warn("bootstrap admin created; initial password written once to stderr", "username", BootstrapUsername)
	return nil
}
