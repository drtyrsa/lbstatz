package musicbrainz

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultBaseURL = "https://musicbrainz.org/ws/2"

	// MaxSearchPerGet is how many artists one search query resolves. 100 arid terms make a
	// ~4.5 KB URL, which the search server accepts, and matches the API's own page limit.
	MaxSearchPerGet = 100

	// minInterval honours MusicBrainz's one-request-per-second policy for anonymous clients.
	// The limit is counted where the requests land, not where they are sent, so pacing at
	// exactly a second leaves ordinary jitter free to drop two of them into the same window.
	// The extra tenth buys that margin, at a cost of a few minutes over a full enrich pass.
	minInterval = 1100 * time.Millisecond

	// maxInterval caps the slowdown applied after a throttling response. A pass with tens of
	// thousands of lookups left is better off crawling to the end than stopping.
	maxInterval = 8 * time.Second

	// intervalStep is how much of the slowdown one successful request wins back, so a burst
	// of throttling does not leave the remaining hours of a run crawling needlessly.
	intervalStep = 100 * time.Millisecond
)

// UserAgent identifies this client to MusicBrainz, which rejects generic agents.
const UserAgent = "lbstatz/0.1 ( https://github.com/drtyrsa/lbstatz )"

type Client struct {
	BaseURL string

	http     *http.Client
	sleep    func(context.Context, time.Duration) error
	now      func() time.Time
	last     time.Time
	interval time.Duration
	status   func(string)
}

// SetStatus registers a sink for a short description of whatever wait the client is
// currently sitting out, called with "" once requests are getting through again. A caller
// drawing a progress line shows it there, so a pass riding out throttling is distinguishable
// from one that has hung. Pass nil to stop.
func (c *Client) SetStatus(f func(string)) { c.status = f }

func (c *Client) setStatus(msg string) {
	if c.status != nil {
		c.status(msg)
	}
}

func New(baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		BaseURL:  baseURL,
		http:     &http.Client{Timeout: 60 * time.Second},
		sleep:    sleep,
		now:      time.Now,
		interval: minInterval,
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

const (
	// maxAttempts bounds retries of a request that failed for a reason other than throttling
	// — a connection error or a 5xx — either of which may well be permanent.
	maxAttempts = 6

	// maxThrottled bounds retries of a throttling response separately, and far more
	// generously, because throttling is by definition temporary. A pass over a large listen
	// history spends hours making requests and will meet it sooner or later; it should ride
	// it out rather than fail and leave the user to restart by hand. The ladder below waits
	// about ten minutes in total before concluding something else is wrong.
	maxThrottled = 12
)

func (c *Client) get(ctx context.Context, endpoint string, out any) error {
	var lastErr error
	failures, throttled := 0, 0
	for failures < maxAttempts && throttled < maxThrottled {
		if err := c.throttle(ctx); err != nil {
			return err
		}

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
			failures++
			lastErr = err
			if err := c.countdown(ctx, backoff(failures), "MusicBrainz unreachable"); err != nil {
				return err
			}
			continue
		}

		// MusicBrainz signals throttling with 503 rather than 429.
		if resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusTooManyRequests {
			throttled++
			delay := retryAfter(resp.Header, backoff(throttled))
			resp.Body.Close()
			lastErr = fmt.Errorf("GET %s: rate limited", endpoint)
			c.slowDown()
			if err := c.countdown(ctx, delay, "throttled by MusicBrainz"); err != nil {
				return err
			}
			continue
		}
		if resp.StatusCode >= 500 {
			status := resp.Status
			resp.Body.Close()
			failures++
			lastErr = fmt.Errorf("GET %s: %s", endpoint, status)
			if err := c.countdown(ctx, backoff(failures), "MusicBrainz returned "+status); err != nil {
				return err
			}
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
		c.speedUp()
		c.setStatus("")
		return nil
	}
	return fmt.Errorf("giving up after %d attempts: %w", failures+throttled, lastErr)
}

// countdown waits for d, keeping the status sink posted on how much of it is left. These
// waits run past a minute, and a line that has not moved for that long reads as a hang
// where a shrinking one reads as what it is.
func (c *Client) countdown(ctx context.Context, d time.Duration, reason string) error {
	if c.status == nil {
		return c.sleep(ctx, d)
	}
	for remaining := d; remaining > 0; {
		c.setStatus(fmt.Sprintf("%s, retrying in %s", reason, remaining.Round(time.Second)))
		step := min(remaining, time.Second)
		if err := c.sleep(ctx, step); err != nil {
			return err
		}
		remaining -= step
	}
	return nil
}

// throttle spaces requests at least the current interval apart, as MusicBrainz requires.
func (c *Client) throttle(ctx context.Context) error {
	if !c.last.IsZero() {
		if wait := c.interval - c.now().Sub(c.last); wait > 0 {
			if err := c.sleep(ctx, wait); err != nil {
				return err
			}
		}
	}
	c.last = c.now()
	return nil
}

// slowDown widens the steady-state interval after a throttling response. Retrying at the
// pace that drew the 503 only earns another one, however long the backoff before it was.
func (c *Client) slowDown() {
	c.interval = min(c.interval*2, maxInterval)
}

// speedUp walks the interval back towards minInterval, one step per request that got through.
func (c *Client) speedUp() {
	c.interval = max(minInterval, c.interval-intervalStep)
}

// retryAfter reads the Retry-After header a throttling response may carry. MusicBrainz asks
// for a single second, which is not enough to clear whatever backlog drew the refusal, so
// the header acts as a floor under our own backoff rather than a replacement for it.
func retryAfter(h http.Header, backoff time.Duration) time.Duration {
	secs, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After")))
	if err != nil || secs < 0 {
		return backoff
	}
	return max(time.Duration(secs)*time.Second, backoff)
}

// backoff grows exponentially and levels off at maxBackoff. A linear 2s..8s ramp gives up
// inside a quarter of a minute, which is nowhere near long enough to outlast MusicBrainz
// throttling a client that still has thousands of lookups to make.
func backoff(attempt int) time.Duration {
	return min(time.Duration(1<<attempt)*time.Second, maxBackoff)
}

const maxBackoff = 90 * time.Second

// sleep waits for d, returning early if ctx is cancelled. Backoff waits run into the
// minutes, and a run left unattended still has to answer Ctrl-C promptly.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
