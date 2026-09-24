package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/drtyrsa/lbstatz/internal/listenbrainz"
	"github.com/drtyrsa/lbstatz/internal/store"
)

func TestCountryFilterCommands(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "lbstatz.toml")
	if err := os.WriteFile(configPath, []byte("username = 'test'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(filepath.Join(dir, "lbstatz.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	now := time.Now()
	if _, err := s.UpsertListens(ctx, []listenbrainz.Listen{
		{ListenedAt: now.Unix(), RecordingMSID: "1", ArtistName: "Band", ArtistMBID: "uk", RecordingMBID: "rec", TrackName: "Song", ReleaseName: "Album"},
		{ListenedAt: now.AddDate(0, 0, -10).Unix(), RecordingMSID: "2", ArtistName: "Band", ArtistMBID: "uk", RecordingMBID: "rec", TrackName: "Song", ReleaseName: "Album"},
		{ListenedAt: now.Unix(), RecordingMSID: "3", ArtistName: "Other", ArtistMBID: "us", RecordingMBID: "rec", TrackName: "Song", ReleaseName: "Album"},
		{ListenedAt: now.Unix(), RecordingMSID: "4", ArtistName: "Unknown", RecordingMBID: "rec", TrackName: "Song", ReleaseName: "Album"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRecordings(ctx, []string{"rec"}, map[string]listenbrainz.RecordingMeta{
		"rec": {
			MBID: "rec", FirstReleaseDate: "1997-01-01",
			Artists:       []listenbrainz.ArtistMeta{{MBID: "uk", Name: "Band"}, {MBID: "us", Name: "Other"}},
			RecordingTags: []listenbrainz.Tag{{Tag: "rock", GenreMBID: "genre", Count: 1}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCountries(ctx, []string{"uk", "us"}, map[string]string{"uk": "GB", "us": "US"}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		args []string
		want string
	}{
		{[]string{"top", "artists"}, `[{"rank":1,"count":2,"name":"Band","mbid":"uk"}]`},
		{[]string{"top", "albums"}, `[{"rank":1,"count":2,"name":"Album","artist":"Band"}]`},
		{[]string{"top", "tracks", "--album", "Album", "--artist", "Band", "--track", "Song"}, `[{"rank":1,"count":2,"name":"Song","artist":"Band","mbid":"rec"}]`},
		{[]string{"top", "artists", "--last", "7"}, `[{"rank":1,"count":1,"name":"Band","mbid":"uk"}]`},
		{[]string{"top", "genres"}, `{"coverage":{"total":2,"covered":2},"rows":[{"rank":1,"count":2,"name":"rock"}]}`},
		{[]string{"top", "countries"}, `{"coverage":{"total":2,"covered":2},"rows":[{"rank":1,"count":2,"name":"United Kingdom","code":"GB"}]}`},
		{[]string{"eras"}, `{"coverage":{"total":2,"covered":2},"rows":[{"start":1990,"listens":2,"bucket_years":10}]}`},
		{[]string{"listens", "--last", "7", "-n", "1"}, ""},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			cmd := NewRootCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs(append(tc.args, "--config", configPath, "--country", "united KINGDOM", "--json"))
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if tc.args[0] == "listens" {
				var rows []store.ListenRow
				if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
					t.Fatal(err)
				}
				if len(rows) != 1 || rows[0].ArtistMBID != "uk" || rows[0].ListenedAt != now.Unix() {
					t.Fatalf("listens = %+v, want the recent UK listen", rows)
				}
				return
			}
			var got, want any
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("output = %s, want %s", out.String(), tc.want)
			}
		})
	}
}
