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

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestSyncFromScratchPaginates(t *testing.T) {
	// Two "full" pages of MaxItemsPerGet, then a short page ends the walk.
	full := listenbrainz.MaxItemsPerGet
	page1 := make([]listenbrainz.Listen, full)
	for i := range page1 {
		ts := int64(10000 - i)
		page1[i] = listen(ts, "a"+strconv.FormatInt(ts, 10))
	}
	page2 := make([]listenbrainz.Listen, full)
	for i := range page2 {
		ts := int64(10000 - full - i)
		page2[i] = listen(ts, "b"+strconv.FormatInt(ts, 10))
	}
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

	n, err := syncListens(context.Background(), src, st, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := 2*full + 1; n != want {
		t.Fatalf("new = %d, want %d", n, want)
	}
	if c, _ := st.Count(context.Background()); c != int64(2*full+1) {
		t.Fatalf("stored = %d", c)
	}
}

func TestSyncIncrementalStopsAtBoundary(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	// Pre-seed one listen; incremental sync should only add newer ones.
	if _, err := st.UpsertListens(ctx, []listenbrainz.Listen{listen(100, "old")}); err != nil {
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
