package geocode

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/reverse_azrieli.json")
	if err != nil {
		t.Fatal(err)
	}
	// The saved file may start with a status line from curl.
	if i := strings.Index(string(b), "{"); i > 0 {
		b = b[i:]
	}
	if i := strings.LastIndex(string(b), "}"); i >= 0 {
		b = b[:i+1]
	}
	return b
}

func TestReverseParsesFixture(t *testing.T) {
	body := fixture(t)
	var gotKey, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotQuery = r.Header.Get("X-Api-Key"), r.URL.RawQuery
		w.Header().Set("X-RateLimit-Remaining", "123")
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	res, info, err := NewClient(srv.URL, "latlng_secret").Reverse(context.Background(), 32.0744874, 34.7918382)
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != `SOFT, תל אביב–יפו` {
		t.Fatalf("name %q", res.Name)
	}
	if !info.RemainingKnown || info.Remaining != 123 {
		t.Fatalf("info %+v", info)
	}
	if gotKey != "latlng_secret" || gotQuery != "lat=32.074487&lon=34.791838" {
		t.Fatalf("key=%q query=%q", gotKey, gotQuery)
	}
}

func TestParseNameFallbacks(t *testing.T) {
	cases := map[string]struct {
		json, want string
		err        error
	}{
		"street":   {`{"features":[{"properties":{"street":"Main","city":"Haifa"}}]}`, "Main, Haifa", nil},
		"dedupe":   {`{"features":[{"properties":{"name":"Haifa","city":"haifa"}}]}`, "Haifa", nil},
		"district": {`{"features":[{"properties":{"district":"D","state":"S","country":"C"}}]}`, "D", nil},
		"state":    {`{"features":[{"properties":{"state":"S","country":"C"}}]}`, "S", nil},
		"country":  {`{"features":[{"properties":{"country":"C"}}]}`, "C", nil},
		"empty":    {`{"features":[{"properties":{}}]}`, "", ErrNoName},
		"none":     {`{"features":[]}`, "", ErrNoName},
	}
	for n, c := range cases {
		got, err := parseName([]byte(c.json))
		if got != c.want || !errors.Is(err, c.err) {
			t.Errorf("%s: got %q, %v", n, got, err)
		}
	}
	if _, err := parseName([]byte(`nope`)); err == nil || errors.Is(err, ErrNoName) {
		t.Errorf("malformed: %v", err)
	}
}

func TestReverseErrors(t *testing.T) {
	var redirects int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("lat") {
		case "1.000000":
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(429)
		case "2.000000":
			w.WriteHeader(401)
		case "3.000000":
			w.WriteHeader(403)
		case "4.000000":
			w.WriteHeader(500)
		case "5.000000":
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		case "6.000000":
			_, _ = w.Write([]byte(strings.Repeat("x", maxBody+10)))
		default:
			redirects++
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "latlng_secret")
	_, _, err := c.Reverse(context.Background(), 1, 0)
	var rl *RateLimitError
	if !errors.Is(err, ErrRateLimited) || !errors.As(err, &rl) || rl.RetryAfter != 30*time.Second {
		t.Fatalf("429: %v", err)
	}
	for _, lat := range []float64{2, 3} {
		if _, _, err := c.Reverse(context.Background(), lat, 0); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("auth %v: %v", lat, err)
		}
	}
	var se *StatusError
	if _, _, err := c.Reverse(context.Background(), 4, 0); !errors.As(err, &se) || se.Code != 500 {
		t.Fatalf("500: %v", err)
	}
	if _, _, err := c.Reverse(context.Background(), 5, 0); !errors.As(err, &se) || se.Code != 302 {
		t.Fatalf("redirect not refused: %v", err)
	}
	if _, _, err := c.Reverse(context.Background(), 6, 0); err == nil {
		t.Fatal("oversized body accepted")
	}
	// Transport errors never mention the key.
	srv.Close()
	if _, _, err := c.Reverse(context.Background(), 0, 0); err == nil || strings.Contains(err.Error(), "latlng_secret") {
		t.Fatalf("transport error: %v", err)
	}
}
