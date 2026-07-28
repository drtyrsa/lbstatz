package listenbrainz

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	DefaultBaseURL = "https://api.listenbrainz.org"
	MaxItemsPerGet = 1000
)

type Listen struct {
	ListenedAt    int64
	RecordingMSID string
	ArtistName    string
	TrackName     string
	ReleaseName   string
	ArtistMBID    string
	ArtistMBIDs   []string
	RecordingMBID string
	ReleaseMBID   string
}

type Page struct {
	Listens  []Listen
	OldestTS int64
	LatestTS int64
}

type Client struct {
	BaseURL  string
	Token    string
	Username string
	http     *http.Client
	sleep    func(time.Duration)
}

func New(baseURL, token, username string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		BaseURL:  baseURL,
		Token:    token,
		Username: username,
		http:     &http.Client{Timeout: 90 * time.Second},
		sleep:    time.Sleep,
	}
}

// Page fetches up to maxItemsPerGet listens older than maxTS (0 means most recent).
func (c *Client) Page(ctx context.Context, maxTS int64) (*Page, error) {
	q := url.Values{}
	q.Set("count", strconv.Itoa(MaxItemsPerGet))
	if maxTS > 0 {
		q.Set("max_ts", strconv.FormatInt(maxTS, 10))
	}
	endpoint := fmt.Sprintf("%s/1/user/%s/listens?%s", c.BaseURL, url.PathEscape(c.Username), q.Encode())

	var resp apiListensResponse
	if err := c.get(ctx, endpoint, &resp); err != nil {
		return nil, err
	}

	page := &Page{
		OldestTS: resp.Payload.OldestListenTS,
		LatestTS: resp.Payload.LatestListenTS,
		Listens:  make([]Listen, 0, len(resp.Payload.Listens)),
	}
	for _, l := range resp.Payload.Listens {
		page.Listens = append(page.Listens, l.normalize())
	}
	return page, nil
}

func (c *Client) ListenCount(ctx context.Context) (int64, error) {
	endpoint := fmt.Sprintf("%s/1/user/%s/listen-count", c.BaseURL, url.PathEscape(c.Username))
	var resp apiCountResponse
	if err := c.get(ctx, endpoint, &resp); err != nil {
		return 0, err
	}
	return resp.Payload.Count, nil
}

const maxAttempts = 6

func (c *Client) get(ctx context.Context, endpoint string, out any) error {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		if c.Token != "" {
			req.Header.Set("Authorization", "Token "+c.Token)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return err
			}
			lastErr = err
			c.sleep(backoff(attempt))
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			delay := resetDelay(resp.Header)
			resp.Body.Close()
			lastErr = fmt.Errorf("GET %s: rate limited", endpoint)
			c.sleep(delay)
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
		remaining := headerInt(resp.Header, "X-RateLimit-Remaining", -1)
		resetIn := resetDelay(resp.Header)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("decoding %s: %w", endpoint, err)
		}
		if remaining == 0 {
			c.sleep(resetIn)
		}
		return nil
	}
	return fmt.Errorf("giving up after %d attempts: %w", maxAttempts, lastErr)
}

func backoff(attempt int) time.Duration {
	return time.Duration(attempt) * 2 * time.Second
}

func resetDelay(h http.Header) time.Duration {
	secs := headerInt(h, "X-RateLimit-Reset-In", 2)
	if secs < 1 {
		secs = 1
	}
	return time.Duration(secs+1) * time.Second
}

func headerInt(h http.Header, key string, fallback int64) int64 {
	v := h.Get(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

type apiListensResponse struct {
	Payload struct {
		Count          int         `json:"count"`
		LatestListenTS int64       `json:"latest_listen_ts"`
		OldestListenTS int64       `json:"oldest_listen_ts"`
		Listens        []apiListen `json:"listens"`
	} `json:"payload"`
}

type apiCountResponse struct {
	Payload struct {
		Count int64 `json:"count"`
	} `json:"payload"`
}

type apiListen struct {
	ListenedAt    int64  `json:"listened_at"`
	RecordingMSID string `json:"recording_msid"`
	TrackMetadata struct {
		ArtistName  string `json:"artist_name"`
		TrackName   string `json:"track_name"`
		ReleaseName string `json:"release_name"`
		Additional  struct {
			ArtistMBIDs   []string `json:"artist_mbids"`
			RecordingMBID string   `json:"recording_mbid"`
			ReleaseMBID   string   `json:"release_mbid"`
		} `json:"additional_info"`
		MBIDMapping struct {
			ArtistMBIDs   []string `json:"artist_mbids"`
			RecordingMBID string   `json:"recording_mbid"`
			ReleaseMBID   string   `json:"release_mbid"`
		} `json:"mbid_mapping"`
	} `json:"track_metadata"`
}

func (a apiListen) normalize() Listen {
	m := a.TrackMetadata
	artistMBIDs := m.MBIDMapping.ArtistMBIDs
	if len(artistMBIDs) == 0 {
		artistMBIDs = m.Additional.ArtistMBIDs
	}
	return Listen{
		ListenedAt:    a.ListenedAt,
		RecordingMSID: a.RecordingMSID,
		ArtistName:    m.ArtistName,
		TrackName:     m.TrackName,
		ReleaseName:   m.ReleaseName,
		ArtistMBID:    first(artistMBIDs),
		ArtistMBIDs:   artistMBIDs,
		RecordingMBID: firstNonEmpty(m.MBIDMapping.RecordingMBID, m.Additional.RecordingMBID),
		ReleaseMBID:   firstNonEmpty(m.Additional.ReleaseMBID, m.MBIDMapping.ReleaseMBID),
	}
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
