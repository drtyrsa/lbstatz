package musicbrainz

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultBaseURL = "https://musicbrainz.org/ws/2"

	// MaxSearchPerGet is how many artists one search query resolves. 100 arid terms make a
	// ~4.5 KB URL, which the search server accepts, and matches the API's own page limit.
	MaxSearchPerGet = 100

	// minInterval honours MusicBrainz's one-request-per-second policy for anonymous clients.
	minInterval = time.Second
)

// UserAgent identifies this client to MusicBrainz, which rejects generic agents.
const UserAgent = "lbstatz/0.1 ( https://github.com/drtyrsa/lbstatz )"

type Client struct {
	BaseURL string
	http    *http.Client
	sleep   func(time.Duration)
	now     func() time.Time
	last    time.Time
}

func New(baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		BaseURL: baseURL,
		http:    &http.Client{Timeout: 60 * time.Second},
		sleep:   time.Sleep,
		now:     time.Now,
	}
}

// Countries resolves ISO 3166-1 alpha-2 country codes for up to MaxSearchPerGet artists in
// one search request. The search index leaves country empty for artists whose area is a
// subdivision (England, Scotland), so callers should fall back to Country for the misses.
func (c *Client) Countries(ctx context.Context, mbids []string) (map[string]string, error) {
	if len(mbids) == 0 {
		return map[string]string{}, nil
	}
	terms := make([]string, 0, len(mbids))
	for _, m := range mbids {
		terms = append(terms, "arid:"+m)
	}
	q := url.Values{}
	q.Set("query", strings.Join(terms, " OR "))
	q.Set("fmt", "json")
	q.Set("limit", fmt.Sprint(MaxSearchPerGet))

	var resp struct {
		Artists []apiArtist `json:"artists"`
	}
	if err := c.get(ctx, c.BaseURL+"/artist?"+q.Encode(), &resp); err != nil {
		return nil, err
	}

	out := make(map[string]string, len(resp.Artists))
	for _, a := range resp.Artists {
		if a.ID != "" && a.Country != "" {
			out[a.ID] = a.Country
		}
	}
	return out, nil
}

// Country resolves one artist's country by direct lookup. Unlike the search index this
// walks up from a subdivision area, so Pink Floyd (area England) resolves to GB.
func (c *Client) Country(ctx context.Context, mbid string) (string, error) {
	var a apiArtist
	if err := c.get(ctx, c.BaseURL+"/artist/"+url.PathEscape(mbid)+"?fmt=json", &a); err != nil {
		return "", err
	}
	if a.Country != "" {
		return a.Country, nil
	}
	// Some artists carry no country but their area does, e.g. one tagged directly with a nation.
	for _, code := range a.Area.ISOCodes {
		if code != "" {
			return code, nil
		}
	}
	return "", nil
}

type apiArtist struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Country string `json:"country"`
	Area    struct {
		Name     string   `json:"name"`
		ISOCodes []string `json:"iso-3166-1-codes"`
	} `json:"area"`
}

const maxAttempts = 4

func (c *Client) get(ctx context.Context, endpoint string, out any) error {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		c.throttle()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", UserAgent)
		req.Header.Set("Accept", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return err
			}
			lastErr = err
			c.sleep(backoff(attempt))
			continue
		}

		// MusicBrainz signals throttling with 503 rather than 429.
		if resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			lastErr = fmt.Errorf("GET %s: rate limited", endpoint)
			c.sleep(backoff(attempt))
			continue
		}
		if resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("GET %s: %s", endpoint, resp.Status)
			c.sleep(backoff(attempt))
			continue
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return fmt.Errorf("GET %s: %s: %s", endpoint, resp.Status, body)
		}

		err = json.NewDecoder(resp.Body).Decode(out)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("decoding %s: %w", endpoint, err)
		}
		return nil
	}
	return fmt.Errorf("giving up after %d attempts: %w", maxAttempts, lastErr)
}

// throttle spaces requests at least minInterval apart, as MusicBrainz requires.
func (c *Client) throttle() {
	if !c.last.IsZero() {
		if wait := minInterval - c.now().Sub(c.last); wait > 0 {
			c.sleep(wait)
		}
	}
	c.last = c.now()
}

func backoff(attempt int) time.Duration {
	return time.Duration(attempt) * 2 * time.Second
}
