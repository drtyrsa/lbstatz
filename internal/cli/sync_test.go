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

	src := &fakeSource{
		total: int64(2*full + 1),
		pages: map[int64]*listenbrainz.Page{
			0:                        {Listens: page1},
			page1[full-1].ListenedAt: {Listens: page2},
			page2[full-1].ListenedAt: {Listens: page3},
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
