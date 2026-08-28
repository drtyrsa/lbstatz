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

	counts, err := syncListens(ctx, src, st, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := 2*full + 1; counts.Inserted != want {
		t.Fatalf("new = %d, want %d", counts.Inserted, want)
	}
	if done, _ := st.BackfillComplete(ctx); !done {
		t.Fatal("backfill should be marked complete")
	}

	// A second sync must only top up (phase 1) and never re-walk the history.
	callsAfterFull := src.calls
	counts2, err := syncListens(ctx, src, st, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if counts2.Inserted != 0 {
		t.Fatalf("second sync added %d, want 0", counts2.Inserted)
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

	counts, err := syncListens(ctx, src, st, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Inserted != len(older) {
		t.Fatalf("new = %d, want %d", counts.Inserted, len(older))
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

	counts, err := syncListens(ctx, src, st, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Inserted != 3 {
		t.Fatalf("new = %d, want 3", counts.Inserted)
	}
	if src.calls != 1 {
		t.Fatalf("expected to stop after 1 page, made %d calls", src.calls)
	}
	if c, _ := st.Count(ctx); c != 4 {
		t.Fatalf("stored = %d, want 4", c)
	}
}

// sliceSource serves listens the way the API does: the newest MaxItemsPerGet listens strictly
// older than max_ts, taken from one descending master list. It records every max_ts asked
// for, which is what the remap sweep's whole value rests on.
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

// mappedHistory builds n listens descending from ts n to 1, every one carrying a recording MBID.
func mappedHistory(n int) []listenbrainz.Listen {
	out := make([]listenbrainz.Listen, n)
	for i := range out {
		ts := int64(n - i)
		out[i] = listen(ts, "m"+strconv.FormatInt(ts, 10))
		out[i].RecordingMBID = "rec" + strconv.FormatInt(ts, 10)
	}
	return out
}

// unmapAt returns a copy of history with the listens at the given timestamps stripped of
// their MBIDs, standing in for what the local database holds after an earlier sync.
func unmapAt(history []listenbrainz.Listen, ts ...int64) []listenbrainz.Listen {
	blank := map[int64]bool{}
	for _, t := range ts {
		blank[t] = true
	}
	out := make([]listenbrainz.Listen, len(history))
	copy(out, history)
	for i := range out {
		if blank[out[i].ListenedAt] {
			out[i].RecordingMBID = ""
		}
	}
	return out
}

func TestRemapJumpsBetweenUnmappedListens(t *testing.T) {
	const n = 5000
	server := mappedHistory(n)
	local := unmapAt(server, 4997, 400) // one gap near the top, one near the bottom

	st := newStore(t)
	ctx := context.Background()
	if _, err := st.UpsertListens(ctx, local); err != nil {
		t.Fatal(err)
	}

	src := &sliceSource{all: server}
	res, err := remapListens(ctx, src, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Remapped != 2 {
		t.Fatalf("remapped = %d, want 2", res.Remapped)
	}
	if res.Inserted != 0 {
		t.Fatalf("inserted = %d, want 0: the sweep must not add listens", res.Inserted)
	}
	if left, _ := st.UnmappedListenCount(ctx); left != 0 {
		t.Fatalf("%d listens still unmapped, want 0", left)
	}

	// The point of the sweep: two requests aimed at the two gaps, rather than the five a
	// full walk of this history would take. max_ts is exclusive, hence the +1 on each.
	want := []int64{4998, 401}
	if len(src.requests) != len(want) {
		t.Fatalf("requests = %v, want %v", src.requests, want)
	}
	for i, w := range want {
		if src.requests[i] != w {
			t.Fatalf("requests = %v, want %v", src.requests, want)
		}
	}
}

func TestRemapTerminatesWhenNothingMaps(t *testing.T) {
	const n = 5000
	// ListenBrainz still has no mapping for these two, as will often be the case.
	server := unmapAt(mappedHistory(n), 4997, 3000)

	st := newStore(t)
	ctx := context.Background()
	if _, err := st.UpsertListens(ctx, server); err != nil {
		t.Fatal(err)
	}

	src := &sliceSource{all: server}
	res, err := remapListens(ctx, src, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Remapped != 0 {
		t.Fatalf("remapped = %d, want 0", res.Remapped)
	}
	if left, _ := st.UnmappedListenCount(ctx); left != 2 {
		t.Fatalf("unmapped = %d, want 2: an unmatched listen stays unmatched", left)
	}
	// The cursor must advance past a page whose gap it failed to fill, or the sweep would
	// request the same page forever.
	want := []int64{4998, 3001}
	if len(src.requests) != len(want) || src.requests[0] != want[0] || src.requests[1] != want[1] {
		t.Fatalf("requests = %v, want %v", src.requests, want)
	}
}

func TestSyncTopUpRemapsTheOverlap(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	// A listen stored earlier that ListenBrainz had not identified yet.
	if _, err := st.UpsertListens(ctx, []listenbrainz.Listen{listen(100, "old")}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetBackfillComplete(ctx, true); err != nil {
		t.Fatal(err)
	}

	mapped := listen(100, "old")
	mapped.RecordingMBID = "rec-old"
	page := []listenbrainz.Listen{listen(101, "n1"), mapped, listen(99, "older")}
	src := &fakeSource{total: 3, pages: map[int64]*listenbrainz.Page{0: {Listens: page}}}

	counts, err := syncListens(ctx, src, st, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Inserted != 1 {
		t.Fatalf("inserted = %d, want 1", counts.Inserted)
	}
	// Plain sync re-fetches the overlap it walks back through, so this costs nothing extra.
	if counts.Remapped != 1 {
		t.Fatalf("remapped = %d, want 1: the top-up overlap should pick up new mappings", counts.Remapped)
	}
	if left, _ := st.UnmappedListenCount(ctx); left != 1 {
		t.Fatalf("unmapped = %d, want 1 (only the new n1)", left)
	}
}

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

	counts, err := syncListens(ctx, src, st, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Inserted != n {
		t.Fatalf("inserted = %d, want %d: a listen sharing the boundary second was dropped", counts.Inserted, n)
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

	counts, err := syncListens(ctx, src, st, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The listens past the page limit inside that one second are unreachable — the API has no
	// way to page within a second — but the walk must get everything below it and stop.
	if want := full + 100; counts.Inserted != want {
		t.Fatalf("inserted = %d, want %d", counts.Inserted, want)
	}
	if len(src.requests) > 4 {
		t.Fatalf("made %d requests (%v), expected the walk to step past the crowded second", len(src.requests), src.requests)
	}
}
