package store

import (
	"context"
	"reflect"
	"testing"

	"github.com/drtyrsa/lbstatz/internal/listenbrainz"
)

func TestCountryFilterUsesPrimaryArtist(t *testing.T) {
	s := seed(t)
	enrich(t, s)
	ctx := context.Background()
	if err := s.SaveArtists(ctx, []string{bjrk, "unknown-country"}, map[string]listenbrainz.ArtistMeta{
		bjrk:              {MBID: bjrk, Name: "Björk"},
		"unknown-country": {MBID: "unknown-country", Name: "Unknown"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCountries(ctx, []string{bjrk}, map[string]string{bjrk: "IS"}); err != nil {
		t.Fatal(err)
	}

	ukPrimary := mk(base+80, "c1", "Radiohead & Björk", "Together", "Collaboration", artistRA, "", "")
	ukPrimary.ArtistMBIDs = []string{artistRA, bjrk}
	isPrimary := mk(base+90, "c2", "Björk & Radiohead", "Together", "Collaboration", bjrk, "", "")
	isPrimary.ArtistMBIDs = []string{bjrk, artistRA}
	if _, err := s.UpsertListens(ctx, []listenbrainz.Listen{
		ukPrimary, isPrimary,
		mk(base+100, "u1", "Unknown", "Missing country", "", "unknown-country", "", ""),
		mk(base+110, "u2", "Unknown", "Missing metadata", "", "missing-artist", "", ""),
		// A known recording or credited name alone must not supply the artist's country.
		mk(base+120, "u3", "Radiohead", "Karma Police", "OK Computer", "", recKP, albOK),
	}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		filter Filter
		limit  int
		want   []int64
	}{
		{"primary UK", Filter{Country: "GB"}, 0, []int64{base + 80, base + 60, base + 50, base + 40, base + 30, base + 20, base + 10}},
		{"primary Iceland", Filter{Country: "IS"}, 0, []int64{base + 90}},
		{"no matches", Filter{Country: "DE"}, 0, nil},
		{"limit after filtering", Filter{Country: "gb"}, 2, []int64{base + 80, base + 60}},
		{"combined filters", Filter{Country: "GB", Artist: "radiohead", Album: albOK, Track: "karma police", From: base + 20, To: base + 30}, 0, []int64{base + 20}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := s.Listens(ctx, tc.filter, tc.limit)
			if err != nil {
				t.Fatal(err)
			}
			var got []int64
			for _, row := range rows {
				got = append(got, row.ListenedAt)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("listen timestamps = %v, want %v", got, tc.want)
			}
		})
	}
}
