package store

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

type Entity int

const (
	Artists Entity = iota
	Albums
	Tracks
	Genres
	Countries
)

func ParseEntity(s string) (Entity, error) {
	switch strings.ToLower(s) {
	case "artists", "artist":
		return Artists, nil
	case "albums", "album":
		return Albums, nil
	case "tracks", "track":
		return Tracks, nil
	case "genres", "genre":
		return Genres, nil
	case "countries", "country":
		return Countries, nil
	default:
		return 0, fmt.Errorf("unknown entity %q (want artists, albums, tracks, genres or countries)", s)
	}
}

// Filter narrows the listen set. Artist/Album/Track accept either a name or an MBID.
type Filter struct {
	From   int64
	To     int64
	Artist string
	Album  string
	Track  string
}

type TopRow struct {
	Rank   int    `json:"rank"`
	Count  int64  `json:"count"`
	Name   string `json:"name"`
	Artist string `json:"artist,omitempty"`
	MBID   string `json:"mbid,omitempty"`
	Code   string `json:"code,omitempty"`
}

type ListenRow struct {
	ListenedAt    int64  `json:"listened_at"`
	ArtistName    string `json:"artist"`
	TrackName     string `json:"track"`
	ReleaseName   string `json:"release,omitempty"`
	ArtistMBID    string `json:"artist_mbid,omitempty"`
	RecordingMBID string `json:"recording_mbid,omitempty"`
	ReleaseMBID   string `json:"release_mbid,omitempty"`
}

var mbidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func IsMBID(s string) bool { return mbidRe.MatchString(s) }

func (f Filter) where() (string, []any) { return f.whereAs("") }

// whereAs builds the filter's WHERE clause with every listens column qualified by alias.
// Stats that join listens against the metadata tables must pass one, because release_mbid
// and release_name exist on both sides and would otherwise be ambiguous.
func (f Filter) whereAs(alias string) (string, []any) {
	q := func(col string) string {
		if alias == "" {
			return col
		}
		return alias + "." + col
	}

	var conds []string
	var args []any

	if f.From > 0 {
		conds = append(conds, q("listened_at")+" >= ?")
		args = append(args, f.From)
	}
	if f.To > 0 {
		conds = append(conds, q("listened_at")+" < ?")
		args = append(args, f.To)
	}
	if f.Artist != "" {
		if IsMBID(f.Artist) {
			conds = append(conds, "instr("+q("artist_mbids")+", ?) > 0")
			args = append(args, ","+f.Artist+",")
		} else {
			conds = append(conds, "lower("+q("artist_name")+") = lower(?)")
			args = append(args, f.Artist)
		}
	}
	addMatch(&conds, &args, f.Album, q("release_mbid"), q("release_name"))
	addMatch(&conds, &args, f.Track, q("recording_mbid"), q("track_name"))

	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func addMatch(conds *[]string, args *[]any, value, mbidCol, nameCol string) {
	if value == "" {
		return
	}
	if IsMBID(value) {
		*conds = append(*conds, mbidCol+" = ?")
		*args = append(*args, value)
		return
	}
	*conds = append(*conds, "lower("+nameCol+") = lower(?)")
	*args = append(*args, value)
}

// andWhere appends an extra condition to a clause built by whereAs, starting one if needed.
func andWhere(clause, cond string) string {
	if clause == "" {
		return " WHERE " + cond
	}
	return clause + " AND " + cond
}

// topArtists groups by artist name (case-insensitive), matching how listens are credited,
// and labels each group with its most frequently used MBID. Grouping by name rather than by
// the contributor MBID set keeps one artist as one row instead of splitting per collaboration.
func (s *Store) topArtists(ctx context.Context, f Filter, limit int) ([]TopRow, error) {
	whereClause, args := f.where()
	guard := "artist_name <> ''"
	if whereClause == "" {
		whereClause = " WHERE " + guard
	} else {
		whereClause += " AND " + guard
	}

	q := `
		SELECT a.name, a.c, COALESCE(m.mbid, '')
		FROM (
			SELECT lower(artist_name) AS lname, MAX(artist_name) AS name, COUNT(*) AS c
			FROM listens` + whereClause + `
			GROUP BY lower(artist_name)
		) a
		LEFT JOIN (
			SELECT lname, mbid FROM (
				SELECT lower(artist_name) AS lname, artist_mbid AS mbid,
					ROW_NUMBER() OVER (PARTITION BY lower(artist_name) ORDER BY COUNT(*) DESC, artist_mbid) AS rn
				FROM listens
				WHERE artist_mbid <> ''
				GROUP BY lower(artist_name), artist_mbid
			) WHERE rn = 1
		) m ON m.lname = a.lname
		ORDER BY a.c DESC, a.name ASC`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := s.query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TopRow
	rank := 0
	for rows.Next() {
		rank++
		r := TopRow{Rank: rank}
		if err := rows.Scan(&r.Name, &r.Count, &r.MBID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Top(ctx context.Context, entity Entity, f Filter, limit int) ([]TopRow, error) {
	if entity == Artists {
		return s.topArtists(ctx, f, limit)
	}

	var keyExpr, nameCol, mbidCol, notEmpty string
	switch entity {
	case Albums:
		keyExpr = "CASE WHEN release_mbid <> '' THEN 'm:'||release_mbid ELSE 'n:'||lower(artist_name)||'|'||lower(release_name) END"
		nameCol = "release_name"
		mbidCol = "release_mbid"
		notEmpty = "(release_name <> '' OR release_mbid <> '')"
	case Tracks:
		keyExpr = "CASE WHEN recording_mbid <> '' THEN 'm:'||recording_mbid ELSE 'n:'||lower(artist_name)||'|'||lower(track_name) END"
		nameCol = "track_name"
		mbidCol = "recording_mbid"
		notEmpty = "(track_name <> '' OR recording_mbid <> '')"
	default:
		return nil, fmt.Errorf("invalid entity")
	}

	whereClause, args := f.where()
	if whereClause == "" {
		whereClause = " WHERE " + notEmpty
	} else {
		whereClause += " AND " + notEmpty
	}

	// Label each group with its most-common (name, artist) pair rather than MAX(), which would
	// surface a lexicographically-largest outlier (e.g. a stray track name in the release field).
	q := fmt.Sprintf(`
		WITH f AS (
			SELECT artist_name, %s AS nm, %s AS mb, %s AS k
			FROM listens%s
		)
		SELECT lbl.name, g.c, lbl.artist, COALESCE(g.mbid, '')
		FROM (SELECT k, COUNT(*) AS c, MAX(mb) AS mbid FROM f GROUP BY k) g
		JOIN (
			SELECT k, nm AS name, artist_name AS artist FROM (
				SELECT k, nm, artist_name,
					ROW_NUMBER() OVER (PARTITION BY k ORDER BY COUNT(*) DESC, nm) AS rn
				FROM f GROUP BY k, nm, artist_name
			) WHERE rn = 1
		) lbl ON lbl.k = g.k
		ORDER BY g.c DESC, lbl.name ASC`,
		nameCol, mbidCol, keyExpr, whereClause)
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := s.query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TopRow
	rank := 0
	for rows.Next() {
		rank++
		var r TopRow
		if err := rows.Scan(&r.Name, &r.Count, &r.Artist, &r.MBID); err != nil {
			return nil, err
		}
		r.Rank = rank
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Listens(ctx context.Context, f Filter, limit int) ([]ListenRow, error) {
	whereClause, args := f.where()
	q := `
		SELECT listened_at, artist_name, track_name, release_name, artist_mbid, recording_mbid, release_mbid
		FROM listens` + whereClause + `
		ORDER BY listened_at DESC`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := s.query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ListenRow
	for rows.Next() {
		var r ListenRow
		if err := rows.Scan(&r.ListenedAt, &r.ArtistName, &r.TrackName, &r.ReleaseName,
			&r.ArtistMBID, &r.RecordingMBID, &r.ReleaseMBID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
