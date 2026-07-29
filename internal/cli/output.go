package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/drtyrsa/lbstatz/internal/store"
)

const maxCol = 60

// clip shortens a string to maxCol runes so one pathological name can't blow up column widths.
func clip(s string) string {
	r := []rune(s)
	if len(r) <= maxCol {
		return s
	}
	return string(r[:maxCol-1]) + "…"
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func printTop(w io.Writer, entity store.Entity, rows []store.TopRow, jsonOut bool) error {
	if jsonOut {
		if rows == nil {
			rows = []store.TopRow{}
		}
		return writeJSON(w, rows)
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	switch entity {
	case store.Artists:
		fmt.Fprintln(tw, "#\tPlays\tArtist")
		for _, r := range rows {
			fmt.Fprintf(tw, "%d\t%d\t%s\n", r.Rank, r.Count, clip(r.Name))
		}
	case store.Albums:
		fmt.Fprintln(tw, "#\tPlays\tAlbum\tArtist")
		for _, r := range rows {
			fmt.Fprintf(tw, "%d\t%d\t%s\t%s\n", r.Rank, r.Count, clip(r.Name), clip(r.Artist))
		}
	case store.Tracks:
		fmt.Fprintln(tw, "#\tPlays\tTrack\tArtist")
		for _, r := range rows {
			fmt.Fprintf(tw, "%d\t%d\t%s\t%s\n", r.Rank, r.Count, clip(r.Name), clip(r.Artist))
		}
	}
	return tw.Flush()
}

func printListens(w io.Writer, rows []store.ListenRow, jsonOut bool) error {
	if jsonOut {
		if rows == nil {
			rows = []store.ListenRow{}
		}
		return writeJSON(w, rows)
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, r := range rows {
		when := time.Unix(r.ListenedAt, 0).Local().Format("2006-01-02 15:04")
		album := clip(r.ReleaseName)
		if album != "" {
			album = "(" + album + ")"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", when, clip(r.ArtistName), clip(r.TrackName), album)
	}
	return tw.Flush()
}
