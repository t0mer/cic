package geocode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the latlng.work API root.
const DefaultBaseURL = "https://api.latlng.work"

const (
	requestTimeout = 10 * time.Second
	maxBody        = 256 << 10
)

var (
	// ErrNoName means the provider answered but nothing in the answer is usable as a name.
	ErrNoName = errors.New("geocode: no place name")
	// ErrUnauthorized means the provider rejected the API key (401/403).
	ErrUnauthorized = errors.New("geocode: api key rejected")
	// ErrRateLimited means the provider answered 429; see RateLimitError for Retry-After.
	ErrRateLimited = errors.New("geocode: rate limited")
)

// RateLimitError is returned for a 429; it matches ErrRateLimited with errors.Is.
type RateLimitError struct {
	RetryAfter time.Duration // zero when the provider sent no usable Retry-After
}

func (e *RateLimitError) Error() string { return ErrRateLimited.Error() }
func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

// StatusError is any other non-200 answer.
type StatusError struct{ Code int }

func (e *StatusError) Error() string { return "geocode: unexpected status " + strconv.Itoa(e.Code) }

// Result is a successful reverse lookup.
type Result struct{ Name string }

// Info is what the provider reports about the quota alongside a result.
type Info struct {
	Remaining      int  // X-RateLimit-Remaining
	RemainingKnown bool // false when the header was absent or malformed
}

// Client calls the reverse endpoint. The key is sent only as a header and never appears in
// errors or logs.
type Client struct {
	baseURL string
	key     string
	http    *http.Client
}

// NewClient returns a Client for baseURL (DefaultBaseURL when empty).
func NewClient(baseURL, key string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), key: key, http: &http.Client{
		Timeout: requestTimeout,
		// A redirect would re-send the key header to wherever it points; treat it as an error.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// Reverse resolves lat/lon to a display name.
func (c *Client) Reverse(ctx context.Context, lat, lon float64) (Result, Info, error) {
	q := url.Values{
		"lat": {strconv.FormatFloat(lat, 'f', 6, 64)},
		"lon": {strconv.FormatFloat(lon, 'f', 6, 64)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/reverse?"+q.Encode(), nil)
	if err != nil {
		return Result{}, Info{}, errors.New("geocode: building request")
	}
	req.Header.Set("X-Api-Key", c.key)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		// url.Error repeats the URL; keep only the cause.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return Result{}, Info{}, fmt.Errorf("geocode: request failed: %w", err)
	}
	defer resp.Body.Close()
	var info Info
	if n, err := strconv.Atoi(resp.Header.Get("X-RateLimit-Remaining")); err == nil && n >= 0 {
		info = Info{Remaining: n, RemainingKnown: true}
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		e := &RateLimitError{}
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			e.RetryAfter = time.Duration(s) * time.Second
		}
		return Result{}, info, e
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return Result{}, info, ErrUnauthorized
	case resp.StatusCode != http.StatusOK:
		return Result{}, info, &StatusError{Code: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return Result{}, info, fmt.Errorf("geocode: reading response: %w", err)
	}
	if len(body) > maxBody {
		return Result{}, info, errors.New("geocode: response too large")
	}
	name, err := parseName(body)
	return Result{Name: name}, info, err
}

type featureCollection struct {
	Features []struct {
		Properties struct {
			Name     string `json:"name"`
			Street   string `json:"street"`
			District string `json:"district"`
			City     string `json:"city"`
			State    string `json:"state"`
			Country  string `json:"country"`
		} `json:"properties"`
	} `json:"features"`
}

// parseName builds the display string from the first feature: "<name or street>, <city>",
// else the first of district, state, country that is set.
func parseName(body []byte) (string, error) {
	var fc featureCollection
	if err := json.Unmarshal(body, &fc); err != nil {
		return "", errors.New("geocode: malformed response")
	}
	if len(fc.Features) == 0 {
		return "", ErrNoName
	}
	p := fc.Features[0].Properties
	var parts []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && !containsFold(parts, s) {
			parts = append(parts, s)
		}
	}
	if strings.TrimSpace(p.Name) != "" {
		add(p.Name)
	} else {
		add(p.Street)
	}
	add(p.City)
	if len(parts) == 0 {
		for _, s := range []string{p.District, p.State, p.Country} {
			if add(s); len(parts) > 0 {
				break
			}
		}
	}
	if len(parts) == 0 {
		return "", ErrNoName
	}
	return strings.Join(parts, ", "), nil
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
