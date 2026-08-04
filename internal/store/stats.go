package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Coverage reports how much of a filtered listen range a stat could actually speak for.
// Listens without the necessary MBID, or whose entities have no metadata, are excluded from
// the numbers above it, so reporting this keeps a partly-enriched database from reading as
// a complete picture.
type Coverage struct {
	Total   int64 `json:"total"`
	Covered int64 `json:"covered"`
}

func (c Coverage) Percent() float64 {
	if c.Total == 0 {
		return 0
	}
	return float64(c.Covered) / float64(c.Total) * 100
}

// genreCTE builds the common-table expressions that map each filtered recording to a genre
// set. Genres are taken from the most specific level that has any: recording tags first,
// then the release group's, then the credited artists'. minVotes is applied before the level
// is chosen, so a recording whose only recording-level tag is below the threshold falls
// through to its release group rather than losing its genres entirely.
func genreCTE(f Filter, minVotes int) (string, []any) {
	whereClause, args := f.where()
	cte := `
	WITH f AS (
		SELECT recording_mbid FROM listens` + whereClause + `
	),
	frec AS (SELECT DISTINCT recording_mbid AS rec FROM f WHERE recording_mbid <> ''),
	cand AS (
		SELECT fr.rec, 1 AS lvl, t.tag
		FROM frec fr
		JOIN tags t ON t.entity_type = 'recording' AND t.entity_mbid = fr.rec
		WHERE t.genre_mbid <> '' AND t.count >= ?
		UNION ALL
		SELECT fr.rec, 2, t.tag
		FROM frec fr
		JOIN recordings r ON r.mbid = fr.rec AND r.release_group_mbid <> ''
		JOIN tags t ON t.entity_type = 'release_group' AND t.entity_mbid = r.release_group_mbid
		WHERE t.genre_mbid <> '' AND t.count >= ?
		UNION ALL
		SELECT fr.rec, 3, t.tag
		FROM frec fr
		JOIN recording_artists ra ON ra.recording_mbid = fr.rec
		JOIN tags t ON t.entity_type = 'artist' AND t.entity_mbid = ra.artist_mbid
		WHERE t.genre_mbid <> '' AND t.count >= ?
	),
	best AS (SELECT rec, MIN(lvl) AS lvl FROM cand GROUP BY rec),
	genres AS (
		SELECT DISTINCT c.rec, c.tag
		FROM cand c JOIN best b ON b.rec = c.rec AND b.lvl = c.lvl
	)`
	return cte, append(args, minVotes, minVotes, minVotes)
}

// TopGenres counts listens per genre. A listen carrying several genres counts toward each,
// so the counts sum to more than the number of listens.
func (s *Store) TopGenres(ctx context.Context, f Filter, minVotes, limit int) ([]TopRow, Coverage, error) {
	cte, args := genreCTE(f, minVotes)

	q := cte + `
		SELECT g.tag, COUNT(*) AS c
		FROM f JOIN genres g ON g.rec = f.recording_mbid
		GROUP BY g.tag
		ORDER BY c DESC, g.tag ASC`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := s.query(ctx, q, args...)
	if err != nil {
		return nil, Coverage{}, err
	}
	defer rows.Close()

	var out []TopRow
	rank := 0
	for rows.Next() {
		rank++
		r := TopRow{Rank: rank}
		if err := rows.Scan(&r.Name, &r.Count); err != nil {
			return nil, Coverage{}, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, Coverage{}, err
	}

	cov, err := s.genreCoverage(ctx, f, minVotes)
	return out, cov, err
}

func (s *Store) genreCoverage(ctx context.Context, f Filter, minVotes int) (Coverage, error) {
	cte, args := genreCTE(f, minVotes)
	q := cte + `
		SELECT
			(SELECT COUNT(*) FROM f),
			(SELECT COUNT(*) FROM f WHERE recording_mbid IN (SELECT rec FROM genres))`

	var c Coverage
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&c.Total, &c.Covered)
	return c, err
}

// TopCountries counts listens by the country of the credited artist. Attribution uses the
// listen's primary artist rather than every collaborator, so one listen counts once.
func (s *Store) TopCountries(ctx context.Context, f Filter, limit int) ([]TopRow, Coverage, error) {
	whereClause, args := f.whereAs("l")

	q := `
		SELECT a.country, COUNT(*) AS c
		FROM listens l
		JOIN artists a ON a.mbid = l.artist_mbid` + andWhere(whereClause, "a.country <> ''") + `
		GROUP BY a.country
		ORDER BY c DESC, a.country ASC`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := s.query(ctx, q, args...)
	if err != nil {
		return nil, Coverage{}, err
	}
	defer rows.Close()

	var out []TopRow
	rank := 0
	for rows.Next() {
		rank++
		r := TopRow{Rank: rank}
		if err := rows.Scan(&r.Code, &r.Count); err != nil {
			return nil, Coverage{}, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, Coverage{}, err
	}

	var c Coverage
	covQ := `
		SELECT COUNT(*),
			COALESCE(SUM(CASE WHEN a.country <> '' THEN 1 ELSE 0 END), 0)
		FROM listens l
		LEFT JOIN artists a ON a.mbid = l.artist_mbid` + whereClause
	if err := s.db.QueryRowContext(ctx, covQ, args...).Scan(&c.Total, &c.Covered); err != nil {
		return nil, Coverage{}, err
	}
	return out, c, nil
}

// Bucket is the granularity of the release-date histogram.
type Bucket int

const (
	ByDecade Bucket = iota
	ByYear
)

func ParseBucket(s string) (Bucket, error) {
	switch strings.ToLower(s) {
	case "decade", "decades":
		return ByDecade, nil
	case "year", "years":
		return ByYear, nil
	default:
		return 0, fmt.Errorf("unknown bucket %q (want decade or year)", s)
	}
}

// EraRow is one bucket of the release-date histogram. Start is the first year the bucket
// covers, so it reads as the year itself when bucketing by year.
type EraRow struct {
	Start  int   `json:"start"`
	Count  int64 `json:"listens"`
	Bucket int   `json:"bucket_years"`
}

// Eras buckets listens by when the recording was first released. Buckets with no listens
// are omitted rather than emitted as zeroes; every row carries its own label, so a gap in
// the sequence is visible without padding sparse catalogues with empty years.
func (s *Store) Eras(ctx context.Context, f Filter, b Bucket) ([]EraRow, Coverage, error) {
	whereClause, args := f.whereAs("l")

	// Hardcoded per bucket, never interpolated from input.
	startExpr, width := "(r.first_release_year / 10) * 10", 10
	if b == ByYear {
		startExpr, width = "r.first_release_year", 1
	}

	q := `
		SELECT ` + startExpr + ` AS start, COUNT(*) AS c
		FROM listens l
		JOIN recordings r ON r.mbid = l.recording_mbid` + andWhere(whereClause, "r.first_release_year > 0") + `
		GROUP BY start
		ORDER BY start ASC`

	rows, err := s.query(ctx, q, args...)
	if err != nil {
		return nil, Coverage{}, err
	}
	defer rows.Close()

	var out []EraRow
	for rows.Next() {
		r := EraRow{Bucket: width}
		if err := rows.Scan(&r.Start, &r.Count); err != nil {
			return nil, Coverage{}, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, Coverage{}, err
	}

	cov, err := s.releaseYearCoverage(ctx, f)
	return out, cov, err
}

// TrendRow summarises, for one calendar year of listening, how old the music was.
type TrendRow struct {
	Year              int   `json:"year"`
	Listens           int64 `json:"listens"`
	MedianReleaseYear int   `json:"median_release_year"`
	MedianAge         int   `json:"median_age"`
}

// EraTrend answers whether listening has drifted toward newer or older music. It reports,
// per year of listening, the median release year and the median age of the music at the
// time it was played. Age is the honest measure of the two: a rising median release year is
// inevitable as time passes, whereas a rising median age means genuinely older listening.
func (s *Store) EraTrend(ctx context.Context, f Filter) ([]TrendRow, Coverage, error) {
	whereClause, args := f.whereAs("l")

	// Grouping in SQL keeps the result small; the medians themselves are computed in Go
	// because SQLite has no median aggregate.
	q := `
		SELECT CAST(strftime('%Y', l.listened_at, 'unixepoch', 'localtime') AS INTEGER) AS ly,
			r.first_release_year AS ry, COUNT(*) AS c
		FROM listens l
		JOIN recordings r ON r.mbid = l.recording_mbid` + andWhere(whereClause, "r.first_release_year > 0") + `
		GROUP BY ly, ry
		ORDER BY ly ASC`

	rows, err := s.query(ctx, q, args...)
	if err != nil {
		return nil, Coverage{}, err
	}
	defer rows.Close()

	type bucket struct {
		year  int
		count int64
	}
	byYear := map[int][]bucket{}
	var order []int
	for rows.Next() {
		var ly, ry int
		var c int64
		if err := rows.Scan(&ly, &ry, &c); err != nil {
			return nil, Coverage{}, err
		}
		if _, seen := byYear[ly]; !seen {
			order = append(order, ly)
		}
		byYear[ly] = append(byYear[ly], bucket{year: ry, count: c})
	}
	if err := rows.Err(); err != nil {
		return nil, Coverage{}, err
	}
	sort.Ints(order)

	out := make([]TrendRow, 0, len(order))
	for _, ly := range order {
		bs := byYear[ly]
		releaseYears := make([]weighted, 0, len(bs))
		var total int64
		for _, b := range bs {
			releaseYears = append(releaseYears, weighted{value: b.year, weight: b.count})
			total += b.count
		}
		// Age is derived from the median release year rather than taken as its own median.
		// Age decreases as release year increases, so with an even number of listens two
		// independent medians tie-break to opposite sides and report contradictory numbers.
		median := weightedMedian(releaseYears)
		out = append(out, TrendRow{
			Year:              ly,
			Listens:           total,
			MedianReleaseYear: median,
			MedianAge:         ly - median,
		})
	}

	cov, err := s.releaseYearCoverage(ctx, f)
	return out, cov, err
}

func (s *Store) releaseYearCoverage(ctx context.Context, f Filter) (Coverage, error) {
	whereClause, args := f.whereAs("l")
	q := `
		SELECT COUNT(*),
			COALESCE(SUM(CASE WHEN r.first_release_year > 0 THEN 1 ELSE 0 END), 0)
		FROM listens l
		LEFT JOIN recordings r ON r.mbid = l.recording_mbid` + whereClause

	var c Coverage
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&c.Total, &c.Covered)
	return c, err
}

type weighted struct {
	value  int
	weight int64
}

// weightedMedian returns the value at the halfway point of the total weight.
func weightedMedian(vs []weighted) int {
	if len(vs) == 0 {
		return 0
	}
	sort.Slice(vs, func(i, j int) bool { return vs[i].value < vs[j].value })
	var total int64
	for _, v := range vs {
		total += v.weight
	}
	half := total / 2
	var run int64
	for _, v := range vs {
		run += v.weight
		if run > half {
			return v.value
		}
	}
	return vs[len(vs)-1].value
}
