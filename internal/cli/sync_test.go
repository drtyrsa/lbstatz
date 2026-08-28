package cli

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/drtyrsa/lbstatz/internal/listenbrainz"
	"github.com/drtyrsa/lbstatz/internal/store"
)

type fakeSource struct {
	pages map[int64]*listenbrainz.Page
	total int64
	calls int
}

func (f *fakeSource) Page(_ context.Context, maxTS int64) (*listenbrainz.Page, error) {
	f.calls++
	if p, ok := f.pages[maxTS]; ok {
		return p, nil
	}
	return &listenbrainz.Page{}, nil
}

func (f *fakeSource) ListenCount(context.Context) (int64, error) { return f.total, nil }

func listen(ts int64, id string) listenbrainz.Listen {
	return listenbrainz.Listen{ListenedAt: ts, RecordingMSID: id, ArtistName: "A", TrackName: id}
}

// descendingPage builds a page of n listens with strictly decreasing timestamps below startTS.
func descendingPage(prefix string, startTS int64, n int) []listenbrainz.Listen {
	out := make([]listenbrainz.Listen, n)
	for i := range out {
		ts := startTS - int64(i)
		out[i] = listen(ts, prefix+strconv.FormatInt(ts, 10))
	}
	return out
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestSyncFullWalkThenTopUpOnly(t *testing.T) {
	full := listenbrainz.MaxItemsPerGet
	page1 := descendingPage("a", 10000, full)
	page2 := descendingPage("b", page1[full-1].ListenedAt-1, full)
	page3 := []listenbrainz.Listen{listen(1, "c1")}

	// The keys carry the +1 the walk puts on its cursor, since max_ts is exclusive and the
	// boundary second has to be re-requested rather than stepped over.
	src := &fakeSource{
		total: int64(2*full + 1),
		pages: map[int64]*listenbrainz.Page{
			0:                            {Listens: page1},
			page1[full-1].ListenedAt + 1: {Listens: page2},
			page2[full-1].ListenedAt + 1: {Listens: page3},
		},
	}
	st := newStore(t)
	ctx := context.Background()

	n, err := syncListens(ctx, src, st, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := 2*full + 1; n != want {
		t.Fatalf("new = %d, want %d", n, want)
	}
	if done, _ := st.BackfillComplete(ctx); !done {
		t.Fatal("backfill should be marked complete")
	}

	// A second sync must only top up (phase 1) and never re-walk the history.
	callsAfterFull := src.calls
	n2, err := syncListens(ctx, src, st, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Fatalf("second sync added %d, want 0", n2)
	}
	if got := src.calls - callsAfterFull; got != 1 {
		t.Fatalf("second sync made %d page calls, want 1 (top-up only)", got)
	}
}

func TestSyncResumesInterruptedBackfill(t *testing.T) {
	full := listenbrainz.MaxItemsPerGet
	newest := descendingPage("a", 10000, full) // the chunk a prior interrupted run stored
	older := descendingPage("b", newest[full-1].ListenedAt-1, 42)

	st := newStore(t)
	ctx := context.Background()

	// Simulate the interrupted state: newest chunk present, backfill not complete.
	if _, err := st.UpsertListens(ctx, newest); err != nil {
		t.Fatal(err)
	}
	if done, _ := st.BackfillComplete(ctx); done {
		t.Fatal("precondition: backfill must be incomplete")
	}

	src := &fakeSource{
		total: int64(full + len(older)),
		pages: map[int64]*listenbrainz.Page{
			0:                             {Listens: newest},
			newest[full-1].ListenedAt + 1: {Listens: older},
		},
	}

	n, err := syncListens(ctx, src, st, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(older) {
		t.Fatalf("new = %d, want %d", n, len(older))
	}
	if c, _ := st.Count(ctx); c != int64(full+len(older)) {
		t.Fatalf("stored = %d, want %d", c, full+len(older))
	}
	if done, _ := st.BackfillComplete(ctx); !done {
		t.Fatal("backfill should now be complete")
	}
}

func TestSyncTopUpStopsAtBoundary(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	if _, err := st.UpsertListens(ctx, []listenbrainz.Listen{listen(100, "old")}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetBackfillComplete(ctx, true); err != nil {
		t.Fatal(err)
	}

	page := []listenbrainz.Listen{
		listen(103, "n3"),
		listen(102, "n2"),
		listen(101, "n1"),
		listen(100, "old"),
		listen(99, "older"),
	}
	src := &fakeSource{total: 6, pages: map[int64]*listenbrainz.Page{0: {Listens: page}}}

	n, err := syncListens(ctx, src, st, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("new = %d, want 3", n)
	}
	if src.calls != 1 {
		t.Fatalf("expected to stop after 1 page, made %d calls", src.calls)
	}
	if c, _ := st.Count(ctx); c != 4 {
		t.Fatalf("stored = %d, want 4", c)
	}
}

// sliceSource serves listens the way the API does: the newest MaxItemsPerGet listens strictly
// older than max_ts, taken from one descending master list. It records every max_ts asked for.
type sliceSource struct {
	all      []listenbrainz.Listen
	requests []int64
}

func (s *sliceSource) Page(_ context.Context, maxTS int64) (*listenbrainz.Page, error) {
	s.requests = append(s.requests, maxTS)
	out := make([]listenbrainz.Listen, 0, listenbrainz.MaxItemsPerGet)
	for _, l := range s.all {
		if maxTS > 0 && l.ListenedAt >= maxTS {
			continue
		}
		out = append(out, l)
		if len(out) == listenbrainz.MaxItemsPerGet {
			break
		}
	}
	return &listenbrainz.Page{Listens: out}, nil
}

func (s *sliceSource) ListenCount(context.Context) (int64, error) { return int64(len(s.all)), nil }

// TestSyncKeepsListensSharingAPageBoundarySecond covers the way a full walk used to lose a
// listen for good. max_ts is exclusive, so resuming a page *at* its oldest listen's timestamp
// asks only for strictly older ones — and any other listen scrobbled in that same second is
// then never fetched, on a history already marked fully downloaded.
func TestSyncKeepsListensSharingAPageBoundarySecond(t *testing.T) {
	full := listenbrainz.MaxItemsPerGet
	const n = 1500

	all := make([]listenbrainz.Listen, n)
	for i := range all {
		ts := int64(n*2 - i)
		all[i] = listen(ts, "msid"+strconv.Itoa(i))
	}
	// Two listens in the same second, straddling the first page boundary.
	all[full].ListenedAt = all[full-1].ListenedAt

	st := newStore(t)
	ctx := context.Background()
	src := &sliceSource{all: all}

	inserted, err := syncListens(ctx, src, st, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if inserted != n {
		t.Fatalf("inserted = %d, want %d: a listen sharing the boundary second was dropped", inserted, n)
	}
	if stored, _ := st.Count(ctx); stored != n {
		t.Fatalf("stored = %d, want %d", stored, n)
	}
}

// TestSyncTerminatesOnAPageFullOfOneSecond guards the fix above: re-requesting the boundary
// second must not turn into an endless loop when a whole page fits inside one.
func TestSyncTerminatesOnAPageFullOfOneSecond(t *testing.T) {
	full := listenbrainz.MaxItemsPerGet
	const crowd = 1100 // more listens in one second than a page can hold

	all := make([]listenbrainz.Listen, 0, crowd+100)
	for i := range crowd {
		all = append(all, listen(5000, "crowd"+strconv.Itoa(i)))
	}
	for i := range 100 {
		all = append(all, listen(int64(4999-i), "tail"+strconv.Itoa(i)))
	}

	st := newStore(t)
	ctx := context.Background()
	src := &sliceSource{all: all}

	inserted, err := syncListens(ctx, src, st, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The listens past the page limit inside that one second are unreachable — the API has no
	// way to page within a second — but the walk must get everything below it and stop.
	if want := full + 100; inserted != want {
		t.Fatalf("inserted = %d, want %d", inserted, want)
	}
	if len(src.requests) > 4 {
		t.Fatalf("made %d requests (%v), expected the walk to step past the crowded second", len(src.requests), src.requests)
	}
}
