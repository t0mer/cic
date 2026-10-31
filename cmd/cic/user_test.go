package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/t0mer/cic/internal/auth"
	"github.com/t0mer/cic/internal/store"
)

// runUser executes `cic user ...` against a temp database with an injected prompt.
func runUser(t *testing.T, dbPath string, prompt termReader, stdin string, args ...string) (string, error) {
	t.Helper()
	open := func(cmd *cobra.Command) (*sql.DB, error) {
		return store.Open(context.Background(), dbPath)
	}
	root := &cobra.Command{Use: "cic", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(newUserCmdWith(userDeps{prompt: prompt, open: open}))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append([]string{"user"}, args...))
	err := root.Execute()
	return out.String(), err
}

func scripted(answers ...string) termReader {
	i := 0
	return func(string) (string, error) {
		if i >= len(answers) {
			return "", errors.New("unexpected prompt")
		}
		i++
		return answers[i-1], nil
	}
}

func TestUserAddResetListDisable(t *testing.T) {
	db := filepath.Join(t.TempDir(), "u.db")
	ctx := context.Background()
	noPrompt := scripted()

	// add from stdin
	if out, err := runUser(t, db, noPrompt, "stdin password 1\n", "add", "alice", "--admin", "--password-stdin"); err != nil || strings.Contains(out, "stdin password") {
		t.Fatalf("add: %v %q", err, out)
	}
	// add via terminal prompt (twice)
	if _, err := runUser(t, db, scripted("terminal password", "terminal password"), "", "add", "vera"); err != nil {
		t.Fatalf("add prompt: %v", err)
	}
	// mismatch, weak and duplicate are refused
	if _, err := runUser(t, db, scripted("terminal password", "different"), "", "add", "bob"); err == nil {
		t.Fatal("mismatched passwords accepted")
	}
	if _, err := runUser(t, db, noPrompt, "short\n", "add", "bob", "--password-stdin"); err == nil {
		t.Fatal("weak password accepted")
	}
	if _, err := runUser(t, db, noPrompt, "stdin password 1\n", "add", "ALICE", "--password-stdin"); err == nil {
		t.Fatal("duplicate accepted")
	}
	if _, err := runUser(t, db, noPrompt, "", "add", "carol", "--password-stdin"); err == nil {
		t.Fatal("empty stdin accepted")
	}

	d, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	st := auth.NewStore(d, nil)
	a, hash, _ := st.UserByName(ctx, "alice")
	v, _, _ := st.UserByName(ctx, "vera")
	if a.Role != auth.RoleAdmin || v.Role != auth.RoleViewer || a.MustChangePassword || !auth.VerifyPassword("stdin password 1", hash) {
		t.Fatalf("created users wrong: %+v %+v", a, v)
	}

	// reset: new password, must change, sessions gone
	tok, _, _ := st.CreateSession(ctx, a.ID, "")
	if out, err := runUser(t, db, noPrompt, "brand new password\n", "reset-password", "alice", "--password-stdin"); err != nil || strings.Contains(out, "brand new") {
		t.Fatalf("reset: %v %q", err, out)
	}
	a, hash, _ = st.UserByName(ctx, "alice")
	if !a.MustChangePassword || !auth.VerifyPassword("brand new password", hash) {
		t.Fatal("reset did not apply")
	}
	if _, _, err := st.Lookup(ctx, tok); !errors.Is(err, auth.ErrNoSession) {
		t.Fatal("reset must invalidate sessions")
	}
	if _, err := runUser(t, db, noPrompt, "x\n", "reset-password", "ghost", "--password-stdin"); err == nil {
		t.Fatal("reset of unknown user accepted")
	}

	// disable / enable / list
	if _, err := runUser(t, db, noPrompt, "", "disable", "vera"); err != nil {
		t.Fatal(err)
	}
	out, err := runUser(t, db, noPrompt, "", "list")
	if err != nil || !strings.Contains(out, "USERNAME") || !regexpLine(out, "vera", "viewer", "true") || !regexpLine(out, "alice", "admin", "false") {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if strings.Contains(out, "$argon2") {
		t.Fatal("list leaks hashes")
	}
	if _, err := runUser(t, db, noPrompt, "", "enable", "vera"); err != nil {
		t.Fatal(err)
	}
	if v, _, _ = st.UserByName(ctx, "vera"); v.Disabled {
		t.Fatal("enable failed")
	}
}

func regexpLine(out string, fields ...string) bool {
	for _, l := range strings.Split(out, "\n") {
		got := strings.Fields(l)
		if len(got) >= len(fields) && got[0] == fields[0] {
			for i, f := range fields {
				if got[i] != f {
					return false
				}
			}
			return true
		}
	}
	return false
}
