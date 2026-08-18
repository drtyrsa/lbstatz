package musicbrainz

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTest returns a client whose waits are recorded rather than actually slept through.
func newTest(baseURL string) (*Client, *[]time.Duration) {
	c := New(baseURL)
	var slept []time.Duration
	now := time.Unix(0, 0)
	c.sleep = func(ctx context.Context, d time.Duration) error {
		slept = append(slept, d)
		now = now.Add(d)
		return ctx.Err()
	}
	c.now = func() time.Time { return now }
	return c, &slept
}

func total(ds []time.Duration) time.Duration {
	var sum time.Duration
	for _, d := range ds {
		sum += d
	}
	return sum
}

func TestCountriesBatchesBySearch(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("query")
		w.Write([]byte(`{"count":2,"artists":[
			{"id":"aaa","name":"Nirvana","country":"US","area":{"name":"United States"}},
			{"id":"bbb","name":"Pink Floyd","area":{"name":"England"}}
		]}`))
	}))
	defer srv.Close()

	c, _ := newTest(srv.URL)
	got, err := c.Countries(context.Background(), []string{"aaa", "bbb"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "arid:aaa OR arid:bbb") {
		t.Errorf("query = %q, want both MBIDs OR-ed", gotQuery)
	}
	if got["aaa"] != "US" {
		t.Errorf("aaa = %q, want US", got["aaa"])
	}
	// The search index reports no country for subdivision areas; those must be left for the
	// direct-lookup fallback rather than being recorded as resolved-with-nothing.
	if _, ok := got["bbb"]; ok {
		t.Errorf("bbb = %q, want omitted so the caller falls back", got["bbb"])
	}
}

func TestCountryDirectLookupResolvesSubdivision(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"bbb","name":"Pink Floyd","country":"GB",
			"area":{"name":"England","iso-3166-2-codes":["GB-ENG"]}}`))
	}))
	defer srv.Close()

	c, _ := newTest(srv.URL)
	got, err := c.Country(context.Background(), "bbb")
	if err != nil {
		t.Fatal(err)
	}
	if got != "GB" {
		t.Errorf("Country = %q, want GB", got)
	}
}

// Some artists carry no country field but their area is itself a nation.
func TestCountryFallsBackToAreaCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"ccc","area":{"name":"Iceland","iso-3166-1-codes":["IS"]}}`))
	}))
	defer srv.Close()

	c, _ := newTest(srv.URL)
	got, err := c.Country(context.Background(), "ccc")
	if err != nil {
		t.Fatal(err)
	}
	if got != "IS" {
		t.Errorf("Country = %q, want IS", got)
	}
}

func TestSendsUserAgent(t *testing.T) {
	var ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		w.Write([]byte(`{"artists":[]}`))
	}))
	defer srv.Close()

	c, _ := newTest(srv.URL)
	if _, err := c.Countries(context.Background(), []string{"aaa"}); err != nil {
		t.Fatal(err)
	}
	// MusicBrainz blocks generic agents outright.
	if ua != UserAgent {
		t.Errorf("User-Agent = %q, want %q", ua, UserAgent)
	}
}

func TestThrottlesBetweenRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"artists":[]}`))
	}))
	defer srv.Close()

	c, slept := newTest(srv.URL)
	for range 3 {
		if _, err := c.Countries(context.Background(), []string{"aaa"}); err != nil {
			t.Fatal(err)
		}
	}
	// The first request goes straight out; each of the next two waits out the interval.
	if len(*slept) != 2 {
		t.Fatalf("slept %v, want two waits", *slept)
	}
	for _, d := range *slept {
		if d != minInterval {
			t.Errorf("wait = %v, want %v", d, minInterval)
		}
	}
}

func TestRetriesOn503(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"artists":[{"id":"aaa","country":"US"}]}`))
	}))
	defer srv.Close()

	c, _ := newTest(srv.URL)
	got, err := c.Countries(context.Background(), []string{"aaa"})
	if err != nil {
		t.Fatal(err)
	}
	if got["aaa"] != "US" {
		t.Errorf("aaa = %q after retry, want US", got["aaa"])
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

// A long unattended pass must outlast a throttling spell rather than make the user restart.
func TestOutlastsSustainedThrottling(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls <= 8 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"artists":[{"id":"aaa","country":"US"}]}`))
	}))
	defer srv.Close()

	c, slept := newTest(srv.URL)
	got, err := c.Countries(context.Background(), []string{"aaa"})
	if err != nil {
		t.Fatal(err)
	}
	if got["aaa"] != "US" {
		t.Errorf("aaa = %q, want US once the throttling lifts", got["aaa"])
	}
	if want := 5 * time.Minute; total(*slept) < want {
		t.Errorf("waited %v across 8 refusals, want at least %v", total(*slept), want)
	}
}

// Throttling and outright failure have separate budgets: a 503 must not spend the retries
// that a genuine server error needs, nor the other way round.
func TestThrottlingDoesNotExhaustErrorBudget(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		// maxAttempts worth of 500s would abort on their own, but they arrive interleaved
		// with 503s and only the last one is fatal.
		if calls%2 == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c, _ := newTest(srv.URL)
	if _, err := c.Countries(context.Background(), []string{"aaa"}); err == nil {
		t.Fatal("Countries succeeded, want the 500s to eventually give up")
	}
	if calls != 2*maxAttempts {
		t.Errorf("calls = %d, want %d: the 503s should not spend the error budget", calls, 2*maxAttempts)
	}
}

// Retrying at the pace that drew the refusal only earns another one.
func TestThrottlingSlowsTheSteadyPace(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"artists":[]}`))
	}))
	defer srv.Close()

	c, slept := newTest(srv.URL)
	for range 2 {
		if _, err := c.Countries(context.Background(), []string{"aaa"}); err != nil {
			t.Fatal(err)
		}
	}
	// One refusal doubles the interval; the success after it wins a step back.
	want := min(minInterval*2, maxInterval) - intervalStep
	if got := (*slept)[len(*slept)-1]; got != want {
		t.Errorf("interval after throttling = %v, want %v", got, want)
	}
}

func TestHonoursRetryAfterAsAFloor(t *testing.T) {
	cases := []struct {
		header  string
		backoff time.Duration
		want    time.Duration
	}{
		{"", 4 * time.Second, 4 * time.Second},
		{"1", 4 * time.Second, 4 * time.Second},   // too short to be worth obeying
		{"120", 4 * time.Second, 2 * time.Minute}, // longer than ours, so obey it
		{"soon", 4 * time.Second, 4 * time.Second},
	}
	for _, tc := range cases {
		h := http.Header{}
		if tc.header != "" {
			h.Set("Retry-After", tc.header)
		}
		if got := retryAfter(h, tc.backoff); got != tc.want {
			t.Errorf("retryAfter(%q, %v) = %v, want %v", tc.header, tc.backoff, got, tc.want)
		}
	}
}

// A caller drawing a progress line needs to know the wait is a wait and not a hang, and
// needs to know when it is over.
func TestReportsThrottlingStatus(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"artists":[]}`))
	}))
	defer srv.Close()

	c, _ := newTest(srv.URL)
	var seen []string
	c.SetStatus(func(s string) { seen = append(seen, s) })

	if _, err := c.Countries(context.Background(), []string{"aaa"}); err != nil {
		t.Fatal(err)
	}
	if len(seen) < 2 {
		t.Fatalf("status updates = %v, want the wait counted down", seen)
	}
	// The countdown ticks once a second, so a 2s backoff says "2s" before it says "1s".
	if !strings.Contains(seen[0], "throttled") || !strings.Contains(seen[0], "2s") {
		t.Errorf("first status = %q, want the reason and the time left", seen[0])
	}
	if !strings.Contains(seen[1], "1s") {
		t.Errorf("second status = %q, want the time left to shrink", seen[1])
	}
	if last := seen[len(seen)-1]; last != "" {
		t.Errorf("final status = %q, want it cleared once a request got through", last)
	}
}

// A backoff can run for minutes, and Ctrl-C has to land before it ends.
func TestBackoffStopsOnCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	c, slept := newTest(srv.URL)
	c.sleep = func(ctx context.Context, d time.Duration) error {
		cancel()
		*slept = append(*slept, d)
		return ctx.Err()
	}

	_, err := c.Countries(ctx, []string{"aaa"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(*slept) != 1 {
		t.Errorf("waited %d times, want to stop at the first one", len(*slept))
	}
}

func TestCountryName(t *testing.T) {
	cases := map[string]string{
		"US": "United States",
		"GB": "United Kingdom",
		"IS": "Iceland",
		"XX": "XX", // unknown codes fall back to themselves rather than vanishing
		"":   "",
	}
	for code, want := range cases {
		if got := CountryName(code); got != want {
			t.Errorf("CountryName(%q) = %q, want %q", code, got, want)
		}
	}
}
