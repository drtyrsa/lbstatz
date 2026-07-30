package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/drtyrsa/lbstatz/internal/store"
	"github.com/spf13/cobra"
)

type filterFlags struct {
	from    string
	to      string
	last    int
	artist  string
	album   string
	track   string
	limit   int
	jsonOut bool
}

func (ff *filterFlags) bind(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&ff.from, "from", "", "start date, inclusive (e.g. 2024-01-01)")
	f.StringVar(&ff.to, "to", "", "end date, inclusive (e.g. 2024-12-31)")
	f.IntVar(&ff.last, "last", 0, "last N days, today included (cannot be used with --from/--to)")
	f.StringVar(&ff.artist, "artist", "", "filter by artist name or MBID")
	f.StringVar(&ff.album, "album", "", "filter by album (release) name or MBID")
	f.StringVar(&ff.track, "track", "", "filter by track name or MBID")
	f.IntVarP(&ff.limit, "limit", "n", 0, "limit rows (0 = all)")
	f.BoolVar(&ff.jsonOut, "json", false, "output JSON instead of text")
}

func (ff *filterFlags) toFilter() (store.Filter, error) {
	if ff.last != 0 && (strings.TrimSpace(ff.from) != "" || strings.TrimSpace(ff.to) != "") {
		return store.Filter{}, fmt.Errorf("--last cannot be combined with --from or --to")
	}
	from, err := parseDate(ff.from, false)
	if err != nil {
		return store.Filter{}, fmt.Errorf("invalid --from: %w", err)
	}
	to, err := parseDate(ff.to, true)
	if err != nil {
		return store.Filter{}, fmt.Errorf("invalid --to: %w", err)
	}
	if ff.last != 0 {
		from, err = lastDaysStart(ff.last, time.Now())
		if err != nil {
			return store.Filter{}, err
		}
	}
	return store.Filter{
		From:   from,
		To:     to,
		Artist: ff.artist,
		Album:  ff.album,
		Track:  ff.track,
	}, nil
}

// lastDaysStart returns the start of the local day n-1 days before now, so that
// --last 1 covers today and --last 7 covers today plus the six previous days.
func lastDaysStart(n int, now time.Time) (int64, error) {
	if n < 1 {
		return 0, fmt.Errorf("invalid --last: %d (expected a number of days >= 1)", n)
	}
	y, m, d := now.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, now.Location()).AddDate(0, 0, -(n - 1)).Unix(), nil
}

// parseDate accepts a date or timestamp in local time. When endExclusive is set a
// bare date resolves to the start of the following day so the whole day is covered.
func parseDate(s string, endExclusive bool) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		if endExclusive {
			t = t.AddDate(0, 0, 1)
		}
		return t.Unix(), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.Unix(), nil
		}
	}
	return 0, fmt.Errorf("unrecognized date %q (use YYYY-MM-DD or RFC3339)", s)
}
