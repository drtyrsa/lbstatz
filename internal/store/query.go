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
)

func ParseEntity(s string) (Entity, error) {
	switch strings.ToLower(s) {
	case "artists", "artist":
		return Artists, nil
	case "albums", "album":
		return Albums, nil
	case "tracks", "track":
		return Tracks, nil
	default:
		return 0, fmt.Errorf("unknown entity %q (want artists, albums or tracks)", s)
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

func (f Filter) where() (string, []any) {
	var conds []string
	var args []any

	if f.From > 0 {
		conds = append(conds, "listened_at >= ?")
		args = append(args, f.From)
	}
	if f.To > 0 {
		conds = append(conds, "listened_at < ?")
		args = append(args, f.To)
	}
	if f.Artist != "" {
		if IsMBID(f.Artist) {
			conds = append(conds, "instr(artist_mbids, ?) > 0")
			args = append(args, ","+f.Artist+",")
		} else {
			conds = append(conds, "lower(artist_name) = lower(?)")
			args = append(args, f.Artist)
		}
	}
	addMatch(&conds, &args, f.Album, "release_mbid", "release_name")
	addMatch(&conds, &args, f.Track, "recording_mbid", "track_name")

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

func (s *Store) Top(ctx context.Context, entity Entity, f Filter, limit int) ([]TopRow, error) {
	var keyExpr, nameCol, artistSelect, mbidCol, notEmpty string
	switch entity {
	case Artists:
		keyExpr = "CASE WHEN artist_mbids <> '' THEN 'm:'||artist_mbids ELSE 'n:'||lower(artist_name) END"
		nameCol = "artist_name"
		artistSelect = "''"
		mbidCol = "artist_mbid"
		notEmpty = "(artist_name <> '' OR artist_mbids <> '')"
	case Albums:
		keyExpr = "CASE WHEN release_mbid <> '' THEN 'm:'||release_mbid ELSE 'n:'||lower(artist_name)||'|'||lower(release_name) END"
		nameCol = "release_name"
		artistSelect = "MAX(artist_name)"
		mbidCol = "release_mbid"
		notEmpty = "(release_name <> '' OR release_mbid <> '')"
	case Tracks:
		keyExpr = "CASE WHEN recording_mbid <> '' THEN 'm:'||recording_mbid ELSE 'n:'||lower(artist_name)||'|'||lower(track_name) END"
		nameCol = "track_name"
		artistSelect = "MAX(artist_name)"
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

	q := fmt.Sprintf(`
		SELECT COUNT(*) AS c, MAX(%s), %s, MAX(%s)
		FROM listens%s
		GROUP BY %s
		ORDER BY c DESC, MAX(%s) ASC`,
		nameCol, artistSelect, mbidCol, whereClause, keyExpr, nameCol)
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
		if err := rows.Scan(&r.Count, &r.Name, &r.Artist, &r.MBID); err != nil {
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
