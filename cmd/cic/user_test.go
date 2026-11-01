package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestUserDisable2FA(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "u.db")
	ctx := context.Background()
	if _, err := runUser(t, dbPath, scripted(), "a long enough password\n", "add", "alice", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	st := auth.NewStore(db, nil)
	k := bytes.Repeat([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18}, 2)[:32]
	st.SetSecretKey(k)
	u, _, _ := st.UserByName(ctx, "alice")
	sec, _ := auth.GenerateSecret()
	if err := st.SetPendingTOTP(ctx, u.ID, sec); err != nil {
		t.Fatal(err)
	}
	c, _ := auth.Code(sec, auth.TOTPStep(time.Now()))
	if _, err := st.EnableTOTP(ctx, u.ID, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	db.Close()

	out, err := runUser(t, dbPath, scripted(), "", "disable-2fa", "alice")
	if err != nil || !strings.Contains(out, "disabled for alice") {
		t.Fatalf("%v %q", err, out)
	}
	db, _ = store.Open(ctx, dbPath)
	defer db.Close()
	if en, rem, _ := auth.NewStore(db, nil).TOTPState(ctx, u.ID); en || rem != 0 {
		t.Fatal("2fa still on")
	}
	if _, err := runUser(t, dbPath, scripted(), "", "disable-2fa", "nobody"); err == nil {
		t.Fatal("unknown user accepted")
	}
}

func TestUserDisable2FAEndsSessions(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "u.db")
	ctx := context.Background()
	if _, err := runUser(t, dbPath, scripted(), "a long enough password\n", "add", "alice", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	st := auth.NewStore(db, nil)
	st.SetSecretKey(bytes.Repeat([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18}, 2)[:32])
	u, _, _ := st.UserByName(ctx, "alice")
	sec, _ := auth.GenerateSecret()
	if err := st.SetPendingTOTP(ctx, u.ID, sec); err != nil {
		t.Fatal(err)
	}
	c, _ := auth.Code(sec, auth.TOTPStep(time.Now()))
	if _, err := st.EnableTOTP(ctx, u.ID, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	tok, _, err := st.CreateSession(ctx, u.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	// A failing revocation must fail the command and change nothing.
	if _, err := db.Exec(`CREATE TRIGGER t BEFORE DELETE ON sessions BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	out, err := runUser(t, dbPath, scripted(), "", "disable-2fa", "alice")
	if err == nil || strings.Contains(out, "disabled for alice") {
		t.Fatalf("claimed recovery despite revocation failure: %v %q", err, out)
	}
	db, _ = store.Open(ctx, dbPath)
	st = auth.NewStore(db, nil)
	if en, _, _ := st.TOTPState(ctx, u.ID); !en {
		t.Fatal("factor cleared although the command failed")
	}
	if _, _, err := st.Check(ctx, tok); err != nil {
		t.Fatalf("session lost on failed command: %v", err)
	}
	if _, err := db.Exec(`DROP TRIGGER t`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	if out, err := runUser(t, dbPath, scripted(), "", "disable-2fa", "alice"); err != nil || !strings.Contains(out, "disabled for alice") {
		t.Fatalf("%v %q", err, out)
	}
	db, _ = store.Open(ctx, dbPath)
	defer db.Close()
	st = auth.NewStore(db, nil)
	if en, _, _ := st.TOTPState(ctx, u.ID); en {
		t.Fatal("2fa still on")
	}
	if _, _, err := st.Check(ctx, tok); !errors.Is(err, auth.ErrNoSession) {
		t.Fatalf("session survived disable-2fa: %v", err)
	}
}
