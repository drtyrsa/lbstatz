package store

import (
	"context"
	"testing"

	"github.com/drtyrsa/lbstatz/internal/listenbrainz"
)

const (
	rgOK = "77777777-7777-7777-7777-777777777777"
	rgKA = "88888888-8888-8888-8888-888888888888"
	bjrk = "99999999-9999-9999-9999-999999999999"
)

func tag(name, genre string, count int) listenbrainz.Tag {
	return listenbrainz.Tag{Tag: name, GenreMBID: genre, Count: count}
}

// enrich gives the seeded listens metadata: Karma Police carries its own recording-level
// genre, No Surprises has none so it must fall back to its release group, and Idioteque has
// neither so it must fall back to the artist.
func enrich(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()

	radiohead := listenbrainz.ArtistMeta{
		MBID: artistRA, Name: "Radiohead", Area: "England", BeginYear: 1985,
		Tags: []listenbrainz.Tag{tag("alternative rock", "g-altrock", 30), tag("oxford", "", 5)},
	}
	metas := map[string]listenbrainz.RecordingMeta{
		recKP: {
			MBID: recKP, Name: "Karma Police", FirstReleaseDate: "1997-05-21",
			ReleaseMBID: albOK, ReleaseGroupMBID: rgOK, ReleaseName: "OK Computer", ReleaseYear: 1997,
			Artists:       []listenbrainz.ArtistMeta{radiohead},
			RecordingTags: []listenbrainz.Tag{tag("art rock", "g-artrock", 4), tag("melancholy", "", 9)},
		},
		recNS: {
			MBID: recNS, Name: "No Surprises", FirstReleaseDate: "1997-05-21",
			ReleaseMBID: albOK, ReleaseGroupMBID: rgOK, ReleaseName: "OK Computer", ReleaseYear: 1997,
			Artists:          []listenbrainz.ArtistMeta{radiohead},
			ReleaseGroupTags: []listenbrainz.Tag{tag("alternative rock", "g-altrock", 21)},
		},
		recID: {
			MBID: recID, Name: "Idioteque", FirstReleaseDate: "2000-10-02",
			ReleaseMBID: albKA, ReleaseGroupMBID: rgKA, ReleaseName: "Kid A", ReleaseYear: 2000,
			Artists: []listenbrainz.ArtistMeta{radiohead},
		},
	}
	if err := s.SaveRecordings(ctx, []string{recKP, recNS, recID}, metas); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCountries(ctx, []string{artistRA}, map[string]string{artistRA: "GB"}); err != nil {
		t.Fatal(err)
	}
}

func genreCount(rows []TopRow, name string) int64 {
	for _, r := range rows {
		if r.Name == name {
			return r.Count
		}
	}
	return 0
}

func TestTopGenresUsesMostSpecificLevel(t *testing.T) {
	s := seed(t)
	enrich(t, s)

	rows, cov, err := s.TopGenres(context.Background(), Filter{}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Karma Police (2 listens) has its own recording tag, so it must not also pick up the
	// release-group or artist genres.
	if got := genreCount(rows, "art rock"); got != 2 {
		t.Errorf("art rock = %d, want 2 (Karma Police only)", got)
	}
	// No Surprises (1) falls back to its release group; Idioteque (3) falls back to the artist.
	// Both land on alternative rock, so 4 listens total.
	if got := genreCount(rows, "alternative rock"); got != 4 {
		t.Errorf("alternative rock = %d, want 4", got)
	}
	// Non-genre tags are folksonomy noise and must never appear.
	if got := genreCount(rows, "melancholy"); got != 0 {
		t.Errorf("melancholy = %d, want 0 (not a genre)", got)
	}
	if got := genreCount(rows, "oxford"); got != 0 {
		t.Errorf("oxford = %d, want 0 (not a genre)", got)
	}

	// 6 Radiohead listens are covered; the Björk listen has no recording MBID.
	if cov.Total != 7 || cov.Covered != 6 {
		t.Errorf("coverage = %+v, want 6 of 7", cov)
	}
}

// A tag below the vote threshold should let a recording fall through to the next level
// rather than stripping its genres entirely.
func TestTopGenresMinVotesFallsThrough(t *testing.T) {
	s := seed(t)
	enrich(t, s)

	rows, _, err := s.TopGenres(context.Background(), Filter{}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := genreCount(rows, "art rock"); got != 0 {
		t.Errorf("art rock = %d, want 0 (4 votes is below the threshold)", got)
	}
	// Karma Police now joins the other two on the release-group/artist genre.
	if got := genreCount(rows, "alternative rock"); got != 6 {
		t.Errorf("alternative rock = %d, want 6", got)
	}
}

func TestTopGenresRespectsFilter(t *testing.T) {
	s := seed(t)
	enrich(t, s)

	rows, _, err := s.TopGenres(context.Background(), Filter{Album: "Kid A"}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := genreCount(rows, "alternative rock"); got != 3 {
		t.Errorf("alternative rock = %d, want 3 Idioteque listens", got)
	}
	if got := genreCount(rows, "art rock"); got != 0 {
		t.Errorf("art rock = %d, want 0 outside Kid A", got)
	}
}

func TestTopCountries(t *testing.T) {
	s := seed(t)
	enrich(t, s)

	rows, cov, err := s.TopCountries(context.Background(), Filter{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Code != "GB" || rows[0].Count != 6 {
		t.Fatalf("rows = %+v, want one GB row with 6 listens", rows)
	}
	// Björk's listen carries no artist MBID, so it cannot be attributed.
	if cov.Total != 7 || cov.Covered != 6 {
		t.Errorf("coverage = %+v, want 6 of 7", cov)
	}
}

func TestEras(t *testing.T) {
	s := seed(t)
	enrich(t, s)

	rows, cov, err := s.Eras(context.Background(), Filter{}, ByDecade)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]int64{1990: 3, 2000: 3}
	if len(rows) != len(want) {
		t.Fatalf("rows = %+v, want %d decades", rows, len(want))
	}
	for _, r := range rows {
		if want[r.Start] != r.Count {
			t.Errorf("decade %d = %d, want %d", r.Start, r.Count, want[r.Start])
		}
		if r.Bucket != 10 {
			t.Errorf("bucket width = %d, want 10", r.Bucket)
		}
	}
	if cov.Covered != 6 {
		t.Errorf("coverage = %+v, want 6 covered", cov)
	}
}

func TestEraTrendReportsMedianAge(t *testing.T) {
	s := seed(t)
	enrich(t, s)

	rows, _, err := s.EraTrend(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want a single listening year", rows)
	}
	// The seeded listens are all in 2020: three from 1997 and three from 2000. The weighted
	// median release year is the upper of the two middle values.
	r := rows[0]
	if r.Year != 2020 || r.Listens != 6 {
		t.Fatalf("row = %+v, want 6 listens in 2020", r)
	}
	if r.MedianReleaseYear != 2000 {
		t.Errorf("MedianReleaseYear = %d, want 2000", r.MedianReleaseYear)
	}
	if r.MedianAge != r.Year-r.MedianReleaseYear {
		t.Errorf("MedianAge = %d, inconsistent with median release year %d", r.MedianAge, r.MedianReleaseYear)
	}
}

func TestWeightedMedian(t *testing.T) {
	cases := []struct {
		name string
		in   []weighted
		want int
	}{
		{"empty", nil, 0},
		{"single", []weighted{{value: 1990, weight: 1}}, 1990},
		{"weight decides", []weighted{{value: 1970, weight: 1}, {value: 2000, weight: 9}}, 2000},
		{"odd count", []weighted{{value: 1, weight: 1}, {value: 2, weight: 1}, {value: 3, weight: 1}}, 2},
		{"unsorted input", []weighted{{value: 3, weight: 1}, {value: 1, weight: 5}}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := weightedMedian(c.in); got != c.want {
				t.Errorf("weightedMedian = %d, want %d", got, c.want)
			}
		})
	}
}

// Misses must be recorded, or every enrich run re-requests the same dead MBIDs forever.
func TestSaveRecordingsRecordsMisses(t *testing.T) {
	s := seed(t)
	ctx := context.Background()

	if err := s.SaveRecordings(ctx, []string{recKP, recNS, recID}, nil); err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingRecordings(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Errorf("pending = %v, want none after recording the misses", pending)
	}
}

func TestPendingConverges(t *testing.T) {
	s := seed(t)
	ctx := context.Background()

	n, err := s.PendingRecordingCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("pending recordings = %d, want 3", n)
	}

	enrich(t, s)

	if n, err = s.PendingRecordingCount(ctx); err != nil || n != 0 {
		t.Errorf("pending recordings after enrich = %d (err %v), want 0", n, err)
	}
	// Enriching recordings also describes their artists, so none are left to fetch directly.
	if n, err = s.PendingArtistCount(ctx); err != nil || n != 0 {
		t.Errorf("pending artists = %d (err %v), want 0", n, err)
	}
	if n, err = s.PendingCountryCount(ctx); err != nil || n != 0 {
		t.Errorf("pending countries = %d (err %v), want 0", n, err)
	}
}

func TestClearMetadataKeepsListens(t *testing.T) {
	s := seed(t)
	ctx := context.Background()
	enrich(t, s)

	if err := s.ClearMetadata(ctx); err != nil {
		t.Fatal(err)
	}
	listens, err := s.Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if listens != 7 {
		t.Errorf("listens = %d, want 7 kept", listens)
	}
	n, err := s.PendingRecordingCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("pending recordings = %d, want 3 back on the queue", n)
	}
}

func TestErasByYear(t *testing.T) {
	s := seed(t)
	enrich(t, s)

	rows, _, err := s.Eras(context.Background(), Filter{}, ByYear)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]int64{1997: 3, 2000: 3}
	if len(rows) != len(want) {
		t.Fatalf("rows = %+v, want %d years", rows, len(want))
	}
	for _, r := range rows {
		if want[r.Start] != r.Count {
			t.Errorf("year %d = %d, want %d", r.Start, r.Count, want[r.Start])
		}
		if r.Bucket != 1 {
			t.Errorf("bucket width = %d, want 1", r.Bucket)
		}
	}
	// Years must come out in order so the histogram reads as a timeline.
	if rows[0].Start > rows[1].Start {
		t.Errorf("rows = %+v, want ascending years", rows)
	}
}

func TestParseBucket(t *testing.T) {
	for _, s := range []string{"decade", "decades", "DECADE"} {
		if got, err := ParseBucket(s); err != nil || got != ByDecade {
			t.Errorf("ParseBucket(%q) = %v, %v", s, got, err)
		}
	}
	for _, s := range []string{"year", "years", "Year"} {
		if got, err := ParseBucket(s); err != nil || got != ByYear {
			t.Errorf("ParseBucket(%q) = %v, %v", s, got, err)
		}
	}
	if _, err := ParseBucket("month"); err == nil {
		t.Error("ParseBucket(\"month\") should fail rather than silently bucket by decade")
	}
}
