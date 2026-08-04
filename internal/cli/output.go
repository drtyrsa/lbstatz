package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
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

// coverageNote explains how much of the filtered range a metadata-backed stat covers, so a
// partly-enriched database never reads as a complete picture.
func coverageNote(c store.Coverage) string {
	if c.Total == 0 {
		return "No listens in range."
	}
	if c.Covered == 0 {
		return fmt.Sprintf("No metadata for any of the %d listens in range — run `lbstatz enrich`.", c.Total)
	}
	note := fmt.Sprintf("Based on %d of %d listens in range (%.1f%%).", c.Covered, c.Total, c.Percent())
	if c.Percent() < 95 {
		note += " Run `lbstatz enrich` to improve coverage."
	}
	return note
}

// covered wraps rows with their coverage for JSON output, keeping the caveat machine-readable
// rather than stranding it in a human-only footer.
type covered struct {
	Coverage store.Coverage `json:"coverage"`
	Rows     any            `json:"rows"`
}

func printTopWithCoverage(w io.Writer, entity store.Entity, rows []store.TopRow, cov store.Coverage, jsonOut bool) error {
	if rows == nil {
		rows = []store.TopRow{}
	}
	if jsonOut {
		return writeJSON(w, covered{Coverage: cov, Rows: rows})
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	label := "Genre"
	if entity == store.Countries {
		label = "Country"
	}
	fmt.Fprintf(tw, "#\tListens\t%s\n", label)
	for _, r := range rows {
		name := clip(r.Name)
		if r.Code != "" {
			name = fmt.Sprintf("%s (%s)", name, r.Code)
		}
		fmt.Fprintf(tw, "%d\t%d\t%s\n", r.Rank, r.Count, name)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "\n%s\n", coverageNote(cov))
	return err
}

const barWidth = 40

func printEras(w io.Writer, rows []store.EraRow, bucket store.Bucket, cov store.Coverage, jsonOut bool) error {
	if rows == nil {
		rows = []store.EraRow{}
	}
	if jsonOut {
		return writeJSON(w, covered{Coverage: cov, Rows: rows})
	}

	var total, max int64
	for _, r := range rows {
		total += r.Count
		if r.Count > max {
			max = r.Count
		}
	}

	header, label := "Decade", "%ds"
	if bucket == store.ByYear {
		header, label = "Year", "%d"
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "%s\tListens\tShare\t\n", header)
	for _, r := range rows {
		share := 0.0
		if total > 0 {
			share = float64(r.Count) / float64(total) * 100
		}
		fmt.Fprintf(tw, label+"\t%d\t%5.1f%%\t%s\n", r.Start, r.Count, share, bar(r.Count, max))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "\n%s\n", coverageNote(cov))
	return err
}

func printTrend(w io.Writer, rows []store.TrendRow, cov store.Coverage, jsonOut bool) error {
	if rows == nil {
		rows = []store.TrendRow{}
	}
	if jsonOut {
		return writeJSON(w, covered{Coverage: cov, Rows: rows})
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Year\tListens\tMedian release\tMedian age")
	for _, r := range rows {
		fmt.Fprintf(tw, "%d\t%d\t%d\t%d yr\n", r.Year, r.Listens, r.MedianReleaseYear, r.MedianAge)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "\n%s\n", coverageNote(cov))
	return err
}

// bar renders a proportional block bar for a value against the largest in the set.
func bar(v, max int64) string {
	if max <= 0 || v <= 0 {
		return ""
	}
	n := int(float64(v) / float64(max) * barWidth)
	if n < 1 {
		n = 1
	}
	return strings.Repeat("█", n)
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
