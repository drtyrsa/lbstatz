package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"testing"

	"github.com/drtyrsa/lbstatz/internal/listenbrainz"
	"github.com/drtyrsa/lbstatz/internal/store"
)

// fakeMeta answers metadata requests from a fixed table and counts what was asked for, so
// tests can assert that a resumed run does not re-request what it already has.
type fakeMeta struct {
	recordings map[string]listenbrainz.RecordingMeta
	artists    map[string]listenbrainz.ArtistMeta
	asked      []string
	failAfter  int // return an error once this many recordings have been requested; 0 disables
}

func (f *fakeMeta) Recordings(ctx context.Context, mbids []string) (map[string]listenbrainz.RecordingMeta, error) {
	f.asked = append(f.asked, mbids...)
	if f.failAfter > 0 && len(f.asked) > f.failAfter {
		return nil, errors.New("boom")
	}
	out := map[string]listenbrainz.RecordingMeta{}
	for _, m := range mbids {
		if r, ok := f.recordings[m]; ok {
			out[m] = r
		}
	}
	return out, nil
}

func (f *fakeMeta) Artists(ctx context.Context, mbids []string) (map[string]listenbrainz.ArtistMeta, error) {
	out := map[string]listenbrainz.ArtistMeta{}
	for _, m := range mbids {
		if a, ok := f.artists[m]; ok {
			out[m] = a
		}
	}
	return out, nil
}

type fakeCountries struct {
	batch     map[string]string // resolved by the search index
	direct    map[string]string // resolved only by direct lookup
	lookups   []string
	failAfter int // fail once this many direct lookups have been made; 0 disables
}

func (f *fakeCountries) Countries(ctx context.Context, mbids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, m := range mbids {
		if c, ok := f.batch[m]; ok {
			out[m] = c
		}
	}
	return out, nil
}

func (f *fakeCountries) SetStatus(func(string)) {}

func (f *fakeCountries) Country(ctx context.Context, mbid string) (string, error) {
	if f.failAfter > 0 && len(f.lookups) >= f.failAfter {
		return "", errors.New("boom")
	}
	f.lookups = append(f.lookups, mbid)
	return f.direct[mbid], nil
}

func mbid(n int) string { return fmt.Sprintf("%08d-0000-0000-0000-000000000000", n) }

// seedListens stores n listens, each on its own recording and artist.
func seedListens(t *testing.T, n int) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	var listens []listenbrainz.Listen
	for i := range n {
		listens = append(listens, listenbrainz.Listen{
			ListenedAt: int64(1_600_000_000 + i), RecordingMSID: fmt.Sprint("msid", i),
			ArtistName: "A", TrackName: "T",
			ArtistMBID: mbid(i), ArtistMBIDs: []string{mbid(i)}, RecordingMBID: mbid(i),
		})
	}
	if _, err := st.UpsertListens(context.Background(), listens); err != nil {
		t.Fatal(err)
	}
	return st
}

func fakeFor(n int) (*fakeMeta, *fakeCountries) {
	meta := &fakeMeta{recordings: map[string]listenbrainz.RecordingMeta{}, artists: map[string]listenbrainz.ArtistMeta{}}
	countries := &fakeCountries{batch: map[string]string{}, direct: map[string]string{}}
	for i := range n {
		meta.recordings[mbid(i)] = listenbrainz.RecordingMeta{
			MBID: mbid(i), Name: "T", FirstReleaseDate: "1999-01-01",
			Artists: []listenbrainz.ArtistMeta{{MBID: mbid(i), Name: "A", Area: "England"}},
		}
		countries.direct[mbid(i)] = "GB"
	}
	return meta, countries
}

func TestEnrichAllFillsEverything(t *testing.T) {
	const n = 5
	st := seedListens(t, n)
	meta, countries := fakeFor(n)

	stats, err := enrichAll(context.Background(), meta, countries, st, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Recordings != n {
		t.Errorf("enriched %d recordings, want %d", stats.Recordings, n)
	}
	// Recording metadata already describes the artists, so the artist pass has nothing to do.
	if stats.Artists != 0 {
		t.Errorf("enriched %d artists, want 0", stats.Artists)
	}
	if stats.Countries != n {
		t.Errorf("resolved %d countries, want %d", stats.Countries, n)
	}

	rows, cov, err := st.TopCountries(context.Background(), store.Filter{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Code != "GB" || rows[0].Count != n {
		t.Errorf("countries = %+v, want one GB row of %d", rows, n)
	}
	if cov.Covered != n {
		t.Errorf("coverage = %+v, want full", cov)
	}
}

// A second run must be a no-op: enrich is driven by what is missing, so re-running it after
// a completed pass should cost nothing.
func TestEnrichAllIsIdempotent(t *testing.T) {
	const n = 3
	st := seedListens(t, n)
	meta, countries := fakeFor(n)

	if _, err := enrichAll(context.Background(), meta, countries, st, io.Discard); err != nil {
		t.Fatal(err)
	}
	asked := len(meta.asked)

	stats, err := enrichAll(context.Background(), meta, countries, st, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !stats.empty() {
		t.Errorf("second run did work: %+v", stats)
	}
	if len(meta.asked) != asked {
		t.Errorf("second run requested %d more recordings, want 0", len(meta.asked)-asked)
	}
}

// An interrupted run must resume where it stopped rather than starting over.
func TestEnrichResumesAfterFailure(t *testing.T) {
	const n = 250 // more than one batch of MaxMetadataPerGet
	st := seedListens(t, n)
	meta, countries := fakeFor(n)
	meta.failAfter = listenbrainz.MaxMetadataPerGet // fail during the second batch

	if _, err := enrichAll(context.Background(), meta, countries, st, io.Discard); err == nil {
		t.Fatal("want an error from the interrupted pass")
	}
	pending, err := st.PendingRecordingCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pending != n-listenbrainz.MaxMetadataPerGet {
		t.Fatalf("pending = %d, want the first batch to have been kept", pending)
	}

	meta.failAfter = 0
	meta.asked = nil
	stats, err := enrichAll(context.Background(), meta, countries, st, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Recordings != n-listenbrainz.MaxMetadataPerGet {
		t.Errorf("resumed run fetched %d, want only the remainder", stats.Recordings)
	}
	for _, m := range meta.asked {
		if m == mbid(0) {
			t.Error("resumed run re-requested a recording from the saved first batch")
		}
	}
	if pending, _ := st.PendingRecordingCount(context.Background()); pending != 0 {
		t.Errorf("pending after resume = %d, want 0", pending)
	}
}

// MBIDs the API has no metadata for must be recorded as misses, or they are retried forever.
func TestEnrichRecordsMissesSoItTerminates(t *testing.T) {
	st := seedListens(t, 3)
	meta := &fakeMeta{recordings: map[string]listenbrainz.RecordingMeta{}, artists: map[string]listenbrainz.ArtistMeta{}}
	countries := &fakeCountries{batch: map[string]string{}, direct: map[string]string{}}

	if _, err := enrichAll(context.Background(), meta, countries, st, io.Discard); err != nil {
		t.Fatal(err)
	}
	if pending, _ := st.PendingRecordingCount(context.Background()); pending != 0 {
		t.Errorf("pending = %d, want 0 even though nothing was found", pending)
	}
	// The artist pass still runs, since listens name artists the empty recordings did not.
	if pending, _ := st.PendingArtistCount(context.Background()); pending != 0 {
		t.Errorf("pending artists = %d, want 0", pending)
	}
}

// The per-artist direct lookup is the expensive path, so it must only run for artists the
// batch search left unresolved and that have an area worth resolving.
func TestEnrichCountriesFallsBackSelectively(t *testing.T) {
	st := seedListens(t, 3)
	meta, countries := fakeFor(3)
	// Artist 0 resolves from the batch search; artist 1 needs the direct lookup;
	// artist 2 has no area at all, so looking it up would be wasted.
	countries.batch[mbid(0)] = "US"
	meta.recordings[mbid(2)] = listenbrainz.RecordingMeta{
		MBID: mbid(2), FirstReleaseDate: "1999-01-01",
		Artists: []listenbrainz.ArtistMeta{{MBID: mbid(2), Name: "A", Area: ""}},
	}

	if _, err := enrichAll(context.Background(), meta, countries, st, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(countries.lookups) != 1 || countries.lookups[0] != mbid(1) {
		t.Errorf("direct lookups = %v, want only the artist with an area and no batch result", countries.lookups)
	}
	if pending, _ := st.PendingCountryCount(context.Background()); pending != 0 {
		t.Errorf("pending countries = %d, want 0", pending)
	}
}

// A batch of direct lookups is one request per artist and takes minutes to get through, so
// failing partway must not throw away the ones that already came back.
func TestEnrichCountriesKeepsPartialBatch(t *testing.T) {
	const n, ok = 5, 3
	ctx := context.Background()
	st := seedListens(t, n)
	meta, countries := fakeFor(n)
	countries.failAfter = ok

	if _, err := enrichAll(ctx, meta, countries, st, io.Discard); err == nil {
		t.Fatal("enrichAll succeeded, want the failing lookup to surface")
	}
	if pending, _ := st.PendingCountryCount(ctx); pending != n-ok {
		t.Errorf("pending countries = %d, want %d: the %d resolved before the failure should be stored", pending, n-ok, ok)
	}

	// The next run picks up exactly where this one stopped rather than starting the batch over.
	countries.failAfter = 0
	countries.lookups = nil
	if _, err := enrichAll(ctx, meta, countries, st, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(countries.lookups) != n-ok {
		t.Errorf("lookups on resume = %d, want %d", len(countries.lookups), n-ok)
	}
	if pending, _ := st.PendingCountryCount(ctx); pending != 0 {
		t.Errorf("pending countries = %d, want 0", pending)
	}
}
