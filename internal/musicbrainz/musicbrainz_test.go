package musicbrainz

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTest returns a client whose throttle is recorded rather than actually slept through.
func newTest(baseURL string) (*Client, *[]time.Duration) {
	c := New(baseURL)
	var slept []time.Duration
	now := time.Unix(0, 0)
	c.sleep = func(d time.Duration) {
		slept = append(slept, d)
		now = now.Add(d)
	}
	c.now = func() time.Time { return now }
	return c, &slept
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

func TestThrottlesToOneRequestPerSecond(t *testing.T) {
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
