package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/drtyrsa/lbstatz/internal/listenbrainz"
)

const (
	artistRA = "11111111-1111-1111-1111-111111111111"
	albOK    = "22222222-2222-2222-2222-222222222222"
	albKA    = "33333333-3333-3333-3333-333333333333"
	recKP    = "44444444-4444-4444-4444-444444444444"
	recNS    = "55555555-5555-5555-5555-555555555555"
	recID    = "66666666-6666-6666-6666-666666666666"

	base = int64(1_600_000_000)
)

func mk(ts int64, msid, artist, track, release, aMBID, rMBID, relMBID string) listenbrainz.Listen {
	l := listenbrainz.Listen{
		ListenedAt: ts, RecordingMSID: msid,
		ArtistName: artist, TrackName: track, ReleaseName: release,
		ArtistMBID: aMBID, RecordingMBID: rMBID, ReleaseMBID: relMBID,
	}
	if aMBID != "" {
		l.ArtistMBIDs = []string{aMBID}
	}
	return l
}

func seed(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	listens := []listenbrainz.Listen{
		mk(base+10, "m1", "Radiohead", "Karma Police", "OK Computer", artistRA, recKP, albOK),
		mk(base+20, "m2", "Radiohead", "Karma Police", "OK Computer", artistRA, recKP, albOK),
		mk(base+30, "m3", "Radiohead", "No Surprises", "OK Computer", artistRA, recNS, albOK),
		mk(base+40, "m4", "Radiohead", "Idioteque", "Kid A", artistRA, recID, albKA),
		mk(base+50, "m5", "Radiohead", "Idioteque", "Kid A", artistRA, recID, albKA),
		mk(base+60, "m6", "Radiohead", "Idioteque", "Kid A", artistRA, recID, albKA),
		mk(base+70, "m7", "Björk", "Army of Me", "Post", "", "", ""),
	}
	if _, err := s.UpsertListens(context.Background(), listens); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestUpsertDedupes(t *testing.T) {
	s := seed(t)
	ctx := context.Background()

	again := []listenbrainz.Listen{
		mk(base+10, "m1", "Radiohead", "Karma Police", "OK Computer", artistRA, recKP, albOK),
		mk(base+80, "m8", "Radiohead", "Fake Plastic Trees", "The Bends", artistRA, "", ""),
	}
	inserted, err := s.UpsertListens(ctx, again)
	if err != nil {
		t.Fatal(err)
	}
	if inserted != 1 {
		t.Fatalf("inserted = %d, want 1", inserted)
	}
	if n, _ := s.Count(ctx); n != 8 {
		t.Fatalf("count = %d, want 8", n)
	}
	if ts, _ := s.MaxListenedAt(ctx); ts != base+80 {
		t.Fatalf("max = %d", ts)
	}
}

func TestTopArtists(t *testing.T) {
	s := seed(t)
	rows, err := s.Top(context.Background(), Artists, Filter{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows", len(rows))
	}
	if rows[0].Name != "Radiohead" || rows[0].Count != 6 {
		t.Errorf("row0 = %+v", rows[0])
	}
	if rows[0].MBID != artistRA {
		t.Errorf("artist mbid = %q", rows[0].MBID)
	}
	if rows[1].Name != "Björk" || rows[1].Count != 1 {
		t.Errorf("row1 = %+v", rows[1])
	}
}

func TestTopAlbumsFilteredByArtist(t *testing.T) {
	s := seed(t)
	// by MBID
	rows, err := s.Top(context.Background(), Albums, Filter{Artist: artistRA}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d albums", len(rows))
	}
	got := map[string]int64{}
	for _, r := range rows {
		got[r.Name] = r.Count
		if r.Artist != "Radiohead" {
			t.Errorf("album %q artist = %q", r.Name, r.Artist)
		}
	}
	if got["OK Computer"] != 3 || got["Kid A"] != 3 {
		t.Fatalf("album counts = %v", got)
	}
}

func TestTopTracksFilteredByAlbumName(t *testing.T) {
	s := seed(t)
	rows, err := s.Top(context.Background(), Tracks, Filter{Album: "ok computer"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d tracks", len(rows))
	}
	if rows[0].Name != "Karma Police" || rows[0].Count != 2 {
		t.Errorf("row0 = %+v", rows[0])
	}
	if rows[1].Name != "No Surprises" || rows[1].Count != 1 {
		t.Errorf("row1 = %+v", rows[1])
	}
}

func TestTopTracksSingleTrackFilter(t *testing.T) {
	s := seed(t)
	rows, err := s.Top(context.Background(), Tracks, Filter{Track: recID}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Count != 3 || rows[0].Name != "Idioteque" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestTextFallbackArtist(t *testing.T) {
	s := seed(t)
	rows, err := s.Top(context.Background(), Tracks, Filter{Artist: "Björk"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Name != "Army of Me" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestDateFilter(t *testing.T) {
	s := seed(t)
	ctx := context.Background()

	rows, err := s.Top(ctx, Artists, Filter{From: base + 35}, 0)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int64{}
	for _, r := range rows {
		counts[r.Name] = r.Count
	}
	if counts["Radiohead"] != 3 || counts["Björk"] != 1 {
		t.Fatalf("from-filtered counts = %v", counts)
	}

	rows, err = s.Top(ctx, Tracks, Filter{To: base + 30}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Name != "Karma Police" || rows[0].Count != 2 {
		t.Fatalf("to-filtered rows = %+v", rows)
	}
}

func TestListens(t *testing.T) {
	s := seed(t)
	rows, err := s.Listens(context.Background(), Filter{Artist: "Radiohead"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d listens", len(rows))
	}
	if rows[0].ListenedAt != base+60 || rows[1].ListenedAt != base+50 {
		t.Fatalf("expected newest first, got %d, %d", rows[0].ListenedAt, rows[1].ListenedAt)
	}
	if rows[0].TrackName != "Idioteque" {
		t.Errorf("track = %q", rows[0].TrackName)
	}
}

func TestCollaborationsAreDistinctButFilterByMembership(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "collab.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()

	guest := "99999999-9999-9999-9999-999999999999"
	solo := mk(base+1, "c1", "Radiohead", "Solo", "X", artistRA, "r1", "")
	collab := mk(base+2, "c2", "Radiohead & Guest", "Together", "Y", artistRA, "r2", "")
	collab.ArtistMBIDs = []string{artistRA, guest}
	collab2 := mk(base+3, "c3", "Radiohead & Guest", "Together", "Y", artistRA, "r2", "")
	collab2.ArtistMBIDs = []string{artistRA, guest}
	if _, err := s.UpsertListens(ctx, []listenbrainz.Listen{solo, collab, collab2}); err != nil {
		t.Fatal(err)
	}

	// Distinct credits => two separate artist rows.
	rows, err := s.Top(ctx, Artists, Filter{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 distinct credits, got %d: %+v", len(rows), rows)
	}

	// Filtering by the shared MBID selects both the solo and the collaboration.
	tracks, err := s.Top(ctx, Tracks, Filter{Artist: artistRA}, 0)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int64{}
	for _, r := range tracks {
		counts[r.Name] = r.Count
	}
	if counts["Solo"] != 1 || counts["Together"] != 2 {
		t.Fatalf("membership filter counts = %v", counts)
	}

	// Filtering by the guest MBID selects only the collaboration.
	tracks, err = s.Top(ctx, Tracks, Filter{Artist: guest}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 1 || tracks[0].Name != "Together" {
		t.Fatalf("guest filter = %+v", tracks)
	}
}

func TestIsMBID(t *testing.T) {
	if !IsMBID(artistRA) {
		t.Error("expected mbid to match")
	}
	for _, s := range []string{"Radiohead", "", "1111", "11111111-1111-1111-1111-11111111111"} {
		if IsMBID(s) {
			t.Errorf("%q should not be an mbid", s)
		}
	}
}
