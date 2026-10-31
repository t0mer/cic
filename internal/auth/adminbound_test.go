package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestSharedHashBoundCapsConcurrentArgon(t *testing.T) {
	s, _ := newSvc(t, false)
	mustUser(t, s, "alice", RoleViewer, false)
	peak, capacity, _, restore := HashLimitsForTests(20*time.Millisecond, 5*time.Second)
	defer restore()
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := range 30 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Store().CreateUser(context.Background(), fmt.Sprintf("user%02d", i), goodPW, RoleViewer, true)
			errs <- err
		}()
	}
	// Logins draw from the same budget and still succeed within the wait.
	loginCodes := make(chan int, 4)
	for range 4 {
		wg.Add(1)
		c := newClient(t, s) // the test client is not goroutine-safe
		go func() { defer wg.Done(); loginCodes <- c.login("alice", goodPW).Code }()
	}
	wg.Wait()
	close(errs)
	close(loginCodes)
	for err := range errs {
		if err != nil {
			t.Errorf("create: %v", err)
		}
	}
	for code := range loginCodes {
		if code != 200 {
			t.Errorf("login while hashing was saturated: %d", code)
		}
	}
	if p := peak(); p > capacity || p < 2 {
		t.Fatalf("peak concurrent argon2 = %d, capacity %d (want 2..%d)", p, capacity, capacity)
	}
}

func TestAdminHashingReturnsErrBusyWhenSaturated(t *testing.T) {
	s, _ := newSvc(t, false)
	u := mustUser(t, s, "alice", RoleViewer, false)
	_, _, saturate, restore := HashLimitsForTests(0, 40*time.Millisecond)
	defer restore()
	release := saturate()
	ctx := context.Background()
	if _, err := s.Store().CreateUser(ctx, "bob", goodPW, RoleViewer, true); !errors.Is(err, ErrBusy) {
		t.Errorf("CreateUser: %v", err)
	}
	if err := s.Store().SetPassword(ctx, u.ID, "alice", "another good password", true); !errors.Is(err, ErrBusy) {
		t.Errorf("SetPassword: %v", err)
	}
	release()
	if _, err := s.Store().CreateUser(ctx, "bob", goodPW, RoleViewer, true); err != nil {
		t.Errorf("after release: %v", err)
	}
	// The unbounded entry point used by the CLI never waits.
	release = saturate()
	defer release()
	if _, err := HashPassword("cli password"); err != nil {
		t.Errorf("HashPassword must not be bounded: %v", err)
	}
}

func TestUpdateUserRollsBackWhenSessionDeleteFails(t *testing.T) {
	s, _ := newSvc(t, false)
	st := s.Store()
	ctx := context.Background()
	mustUser(t, s, "root", RoleAdmin, false)
	target := mustUser(t, s, "other", RoleAdmin, false)
	if _, _, err := st.CreateSession(ctx, target.ID, "ua"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`CREATE TRIGGER no_session_delete BEFORE DELETE ON sessions
		BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	viewer, dis := RoleViewer, true
	for _, c := range []struct {
		role *Role
		dis  *bool
	}{{&viewer, nil}, {nil, &dis}} {
		if _, err := st.UpdateUser(ctx, target.ID, c.role, c.dis); err == nil {
			t.Fatal("expected an error")
		}
		got, _ := st.UserByID(ctx, target.ID)
		if got.Role != RoleAdmin || got.Disabled {
			t.Fatalf("change survived a failed session delete: %+v", got)
		}
	}
	var n int
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id = ?`, target.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("session count %d", n)
	}
	if _, err := st.db.Exec(`DROP TRIGGER no_session_delete`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateUser(ctx, target.ID, &viewer, nil); err != nil {
		t.Fatal(err)
	}
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id = ?`, target.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("sessions not dropped on success: %d", n)
	}
}
