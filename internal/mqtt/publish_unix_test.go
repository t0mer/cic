//go:build !windows

package mqtt

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"
)

func TestPublishTimeoutIsNotProvenUnsent(t *testing.T) {
	c, _, cmd, _ := connectedClient(t)
	// A stopped broker never acknowledges, so the wait must end on the context.
	if err := cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Signal(syscall.SIGCONT)
	tctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := c.Publish(tctx, "dradis/p1/ping", []byte("{}"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want deadline exceeded", err)
	}
	if NotSent(err) {
		t.Errorf("timeout must not be reported as not sent: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("publish ignored the context deadline: %v", time.Since(start))
	}
}
