package listenbrainz

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const sampleListens = `{
  "payload": {
    "count": 1,
    "latest_listen_ts": 1771414109,
    "oldest_listen_ts": 1160599019,
    "listens": [
      {
        "listened_at": 1771414109,
        "recording_msid": "fd0cad33-ea33-453b-8155-1292379277db",
        "track_metadata": {
          "artist_name": "Röyksopp",
          "track_name": "Some Resolve",
          "release_name": "Profound Mysteries II",
          "additional_info": {
            "artist_mbids": ["1c70a3fc-fa3c-4be1-8b55-c3192db8a884"],
            "recording_mbid": "aaaa1111-d825-4ae1-b79c-44242cddd7c0",
            "release_mbid": "f1418001-7f1e-46af-bfdb-95faeded8841"
          },
          "mbid_mapping": {
            "artist_mbids": ["1c70a3fc-fa3c-4be1-8b55-c3192db8a884"],
            "recording_mbid": "30d08f4c-d825-4ae1-b79c-44242cddd7c0",
            "release_mbid": "e96c2e7b-94fd-4fbe-9c44-1a27d8664825"
          }
        }
      }
    ]
  }
}`

func TestPageParsesAndPrefersMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("count"); got != "1000" {
			t.Errorf("count = %q, want 1000", got)
		}
		if got := r.URL.Query().Get("max_ts"); got != "1700000000" {
			t.Errorf("max_ts = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Token secret" {
			t.Errorf("auth = %q", got)
		}
		w.Write([]byte(sampleListens))
	}))
	defer srv.Close()

	c := New(srv.URL, "secret", "alice")
	page, err := c.Page(context.Background(), 1700000000)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Listens) != 1 {
		t.Fatalf("got %d listens", len(page.Listens))
	}
	l := page.Listens[0]
	if l.ArtistName != "Röyksopp" || l.TrackName != "Some Resolve" || l.ReleaseName != "Profound Mysteries II" {
		t.Fatalf("bad metadata: %+v", l)
	}
	if l.RecordingMBID != "30d08f4c-d825-4ae1-b79c-44242cddd7c0" {
		t.Errorf("recording mbid = %q, want mapping value", l.RecordingMBID)
	}
	if l.ReleaseMBID != "f1418001-7f1e-46af-bfdb-95faeded8841" {
		t.Errorf("release mbid = %q, want submitted value", l.ReleaseMBID)
	}
	if page.OldestTS != 1160599019 {
		t.Errorf("oldest ts = %d", page.OldestTS)
	}
}

func TestPageRetriesOnRateLimit(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("X-RateLimit-Reset-In", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(sampleListens))
	}))
	defer srv.Close()

	c := New(srv.URL, "", "alice")
	var slept time.Duration
	c.sleep = func(d time.Duration) { slept += d }

	if _, err := c.Page(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("expected retry, got %d calls", calls)
	}
	if slept <= 0 {
		t.Fatal("expected to sleep before retry")
	}
}

func TestListenCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"payload":{"count":4242}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "", "alice")
	n, err := c.ListenCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 4242 {
		t.Fatalf("count = %d", n)
	}
}
