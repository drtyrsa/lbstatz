package listenbrainz

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const sampleRecordingMeta = `{
  "0ef2f43e-51d4-4930-9052-3820358044d5": {
    "artist": {
      "artist_credit_id": 54,
      "name": "Nirvana",
      "artists": [
        {"area": "United States", "artist_mbid": "5b11f4ce-a62d-471e-81fc-a69a8278c7da",
         "begin_year": 1987, "end_year": 1994, "name": "Nirvana", "type": "Group"},
        {"area": "Berlin", "artist_mbid": "aaaaaaaa-a62d-471e-81fc-a69a8278c7da",
         "name": "Someone Else", "type": "Person"}
      ]
    },
    "recording": {"first_release_date": "1991-09-10", "length": 279933, "name": "Smells Like Teen Spirit"},
    "release": {"mbid": "eccae410-7577-4daa-b602-92d305828331", "name": "Nevermind",
                "release_group_mbid": "1b022e01-4da6-387b-8658-8678046e4cef", "year": 1991},
    "tag": {
      "artist": [
        {"artist_mbid": "5b11f4ce-a62d-471e-81fc-a69a8278c7da", "count": 64,
         "genre_mbid": "3f08faa9-6c3e-4b6d-b4b8-08eea45954e2", "tag": "grunge"},
        {"artist_mbid": "5b11f4ce-a62d-471e-81fc-a69a8278c7da", "count": 6, "tag": "seattle"},
        {"artist_mbid": "aaaaaaaa-a62d-471e-81fc-a69a8278c7da", "count": 2,
         "genre_mbid": "0e3fc579-2d24-4f20-9dae-736e1ec78798", "tag": "techno"}
      ],
      "recording": [{"count": 3, "genre_mbid": "3f08faa9-6c3e-4b6d-b4b8-08eea45954e2", "tag": "grunge"}],
      "release_group": [{"count": 21, "genre_mbid": "ceeaa283-5d7b-4202-8d1d-e25d116b2a18", "tag": "alternative rock"}]
    }
  }
}`

func TestRecordingsParsesMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("inc"); got != "artist tag release" {
			t.Errorf("inc = %q, want %q", got, "artist tag release")
		}
		if got := r.URL.Query().Get("recording_mbids"); got == "" {
			t.Error("recording_mbids missing")
		}
		w.Write([]byte(sampleRecordingMeta))
	}))
	defer srv.Close()

	c := New(srv.URL, "", "u")
	got, err := c.Recordings(context.Background(), []string{"0ef2f43e-51d4-4930-9052-3820358044d5"})
	if err != nil {
		t.Fatal(err)
	}

	m, ok := got["0ef2f43e-51d4-4930-9052-3820358044d5"]
	if !ok {
		t.Fatal("recording missing from result")
	}
	if m.FirstReleaseDate != "1991-09-10" {
		t.Errorf("FirstReleaseDate = %q", m.FirstReleaseDate)
	}
	if m.ReleaseGroupMBID != "1b022e01-4da6-387b-8658-8678046e4cef" {
		t.Errorf("ReleaseGroupMBID = %q", m.ReleaseGroupMBID)
	}
	if len(m.Artists) != 2 {
		t.Fatalf("got %d artists, want 2", len(m.Artists))
	}
	if m.Artists[0].Area != "United States" || m.Artists[0].BeginYear != 1987 {
		t.Errorf("artist 0 = %+v", m.Artists[0])
	}

	// Artist tags arrive as one flat list for the whole credit and must be split by MBID,
	// or every collaborator inherits the others' genres.
	if len(m.Artists[0].Tags) != 2 {
		t.Errorf("artist 0 tags = %+v, want the 2 tagged with its own MBID", m.Artists[0].Tags)
	}
	if len(m.Artists[1].Tags) != 1 || m.Artists[1].Tags[0].Tag != "techno" {
		t.Errorf("artist 1 tags = %+v, want only techno", m.Artists[1].Tags)
	}
	if len(m.RecordingTags) != 1 || m.RecordingTags[0].Tag != "grunge" {
		t.Errorf("RecordingTags = %+v", m.RecordingTags)
	}
	if len(m.ReleaseGroupTags) != 1 || m.ReleaseGroupTags[0].GenreMBID == "" {
		t.Errorf("ReleaseGroupTags = %+v", m.ReleaseGroupTags)
	}
}

// The API omits MBIDs it has no metadata for rather than returning an empty entry.
func TestRecordingsOmitsUnknownMBIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	got, err := New(srv.URL, "", "u").Recordings(context.Background(), []string{"missing-mbid"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %d results, want 0", len(got))
	}
}

func TestArtistsParsesMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"artist_mbid": "5b11f4ce-a62d-471e-81fc-a69a8278c7da", "name": "Nirvana",
			"area": "United States", "begin_year": 1987, "type": "Group",
			"tag": {"artist": [{"artist_mbid": "5b11f4ce-a62d-471e-81fc-a69a8278c7da", "count": 64,
				"genre_mbid": "3f08faa9-6c3e-4b6d-b4b8-08eea45954e2", "tag": "grunge"}]}}]`))
	}))
	defer srv.Close()

	got, err := New(srv.URL, "", "u").Artists(context.Background(), []string{"5b11f4ce-a62d-471e-81fc-a69a8278c7da"})
	if err != nil {
		t.Fatal(err)
	}
	a, ok := got["5b11f4ce-a62d-471e-81fc-a69a8278c7da"]
	if !ok {
		t.Fatal("artist missing from result")
	}
	if a.Area != "United States" || a.Name != "Nirvana" {
		t.Errorf("artist = %+v", a)
	}
	if len(a.Tags) != 1 || a.Tags[0].Tag != "grunge" {
		t.Errorf("tags = %+v", a.Tags)
	}
}

func TestParseYear(t *testing.T) {
	cases := map[string]int{
		"1991-09-10": 1991,
		"1991-09":    1991,
		"1991":       1991,
		"":           0,
		"19":         0,
		"not-a-date": 0,
		"0042":       0,
	}
	for in, want := range cases {
		if got := ParseYear(in); got != want {
			t.Errorf("ParseYear(%q) = %d, want %d", in, got, want)
		}
	}
}

// A batch the gateway rejects must be retried in halves. Re-sending the identical request,
// which is all a plain retry does, can never get past a batch that is simply too heavy.
func TestRecordingsSplitsRejectedBatch(t *testing.T) {
	var sizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ids := strings.Split(r.URL.Query().Get("recording_mbids"), ",")
		sizes = append(sizes, len(ids))
		if len(ids) > 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		out := map[string]any{}
		for _, id := range ids {
			out[id] = map[string]any{"recording": map[string]any{"first_release_date": "1991"}}
		}
		json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()

	c := New(srv.URL, "", "u")
	c.sleep = func(time.Duration) {}

	got, err := c.Recordings(context.Background(), []string{"a", "b", "c", "d"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Errorf("got %d recordings, want all 4 across the split batches", len(got))
	}
	// The full batch is attempted (and retried) before being halved into two that succeed.
	if sizes[len(sizes)-1] != 2 || sizes[len(sizes)-2] != 2 {
		t.Errorf("request sizes = %v, want the last two to be halves", sizes)
	}
}

// Splitting must be bounded, or a real outage fans out into a flood of doomed requests.
func TestRecordingsStopsSplitting(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	c := New(srv.URL, "", "u")
	c.sleep = func(time.Duration) {}

	if _, err := c.Recordings(context.Background(), []string{"a", "b", "c", "d"}); err == nil {
		t.Fatal("want an error when every request fails")
	}
	// The first failing half aborts the batch, so this walks one chain of splits rather than
	// the whole tree. Each divisible batch gets the short budget; only the indivisible one at
	// the end is worth the full retry ladder.
	if want := maxBatchSplits*attemptsBeforeSplit + maxAttempts; calls != want {
		t.Errorf("made %d requests, want %d", calls, want)
	}
}

// A 4xx is the client's fault and cannot be fixed by sending less, so it must not split.
func TestRecordingsDoesNotSplitClientError(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	c := New(srv.URL, "", "u")
	c.sleep = func(time.Duration) {}

	if _, err := c.Recordings(context.Background(), []string{"a", "b", "c", "d"}); err == nil {
		t.Fatal("want an error")
	}
	if calls != 1 {
		t.Errorf("made %d requests, want 1 with no retry or split", calls)
	}
}

func TestBackoffGrowsExponentially(t *testing.T) {
	prev := time.Duration(0)
	for attempt := 1; attempt <= 8; attempt++ {
		d := backoff(attempt)
		if d < prev {
			t.Errorf("backoff(%d) = %v, shrank from %v", attempt, d, prev)
		}
		if d > maxBackoff {
			t.Errorf("backoff(%d) = %v, exceeds the %v cap", attempt, d, maxBackoff)
		}
		prev = d
	}
	// A linear ramp gave up in about half a minute, too short for a routine upstream blip.
	var total time.Duration
	for attempt := 1; attempt < maxAttempts; attempt++ {
		total += backoff(attempt)
	}
	if total < 60*time.Second {
		t.Errorf("total wait before giving up = %v, want at least a minute", total)
	}
}

// Splitting is the effective remedy for a rejected batch, so it must be reached quickly
// rather than after the full retry ladder has been exhausted.
func TestRecordingsSplitsBeforeLongBackoff(t *testing.T) {
	var attemptsOnFull int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ids := strings.Split(r.URL.Query().Get("recording_mbids"), ",")
		if len(ids) > 2 {
			attemptsOnFull++
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		out := map[string]any{}
		for _, id := range ids {
			out[id] = map[string]any{"recording": map[string]any{"first_release_date": "1991"}}
		}
		json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()

	var waited time.Duration
	c := New(srv.URL, "", "u")
	c.sleep = func(d time.Duration) { waited += d }

	if _, err := c.Recordings(context.Background(), []string{"a", "b", "c", "d"}); err != nil {
		t.Fatal(err)
	}
	if attemptsOnFull != attemptsBeforeSplit {
		t.Errorf("tried the full batch %d times, want %d before splitting", attemptsOnFull, attemptsBeforeSplit)
	}
	// The full ladder would burn about a minute here.
	if waited > 10*time.Second {
		t.Errorf("waited %v before splitting, want the short budget", waited)
	}
}
