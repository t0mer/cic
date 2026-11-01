package main

import (
	"testing"
	"time"
)

func TestTimezoneDatabaseEmbedded(t *testing.T) {
	if _, err := time.LoadLocation("Asia/Jerusalem"); err != nil {
		t.Fatal(err)
	}
}
