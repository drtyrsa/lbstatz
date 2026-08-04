package store

import (
	"context"
	"database/sql"
	"strings"

	"github.com/drtyrsa/lbstatz/internal/listenbrainz"
)

// Tag entity types, matching the levels ListenBrainz reports tags at.
const (
	TagRecording    = "recording"
	TagReleaseGroup = "release_group"
	TagArtist       = "artist"
)

// PendingRecordings returns up to limit recording MBIDs seen in listens but not yet
// looked up. Because saving a batch removes it from this set, callers can loop until empty.
func (s *Store) PendingRecordings(ctx context.Context, limit int) ([]string, error) {
	return s.pendingMBIDs(ctx, `
		SELECT DISTINCT l.recording_mbid
		FROM listens l
		LEFT JOIN recordings r ON r.mbid = l.recording_mbid
		WHERE l.recording_mbid <> '' AND r.mbid IS NULL
		LIMIT ?`, limit)
}

func (s *Store) PendingRecordingCount(ctx context.Context) (int64, error) {
	return s.countRows(ctx, `
		SELECT COUNT(*) FROM (
			SELECT DISTINCT l.recording_mbid
			FROM listens l
			LEFT JOIN recordings r ON r.mbid = l.recording_mbid
			WHERE l.recording_mbid <> '' AND r.mbid IS NULL)`)
}

// PendingArtists returns artist MBIDs credited on listens that no recording lookup has
// already described.
func (s *Store) PendingArtists(ctx context.Context, limit int) ([]string, error) {
	return s.pendingMBIDs(ctx, `
		SELECT DISTINCT l.artist_mbid
		FROM listens l
		LEFT JOIN artists a ON a.mbid = l.artist_mbid
		WHERE l.artist_mbid <> '' AND a.mbid IS NULL
		LIMIT ?`, limit)
}

func (s *Store) PendingArtistCount(ctx context.Context) (int64, error) {
	return s.countRows(ctx, `
		SELECT COUNT(*) FROM (
			SELECT DISTINCT l.artist_mbid
			FROM listens l
			LEFT JOIN artists a ON a.mbid = l.artist_mbid
			WHERE l.artist_mbid <> '' AND a.mbid IS NULL)`)
}

// ArtistRef identifies an artist awaiting country resolution. Area comes from ListenBrainz
// and tells the caller whether a direct MusicBrainz lookup could still help.
type ArtistRef struct {
	MBID string
	Area string
}

// PendingCountries returns artists the MusicBrainz country pass has not visited yet.
func (s *Store) PendingCountries(ctx context.Context, limit int) ([]ArtistRef, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.query(ctx, `
		SELECT mbid, area FROM artists WHERE country_resolved = 0 LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ArtistRef
	for rows.Next() {
		var a ArtistRef
		if err := rows.Scan(&a.MBID, &a.Area); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) PendingCountryCount(ctx context.Context) (int64, error) {
	return s.countRows(ctx, `SELECT COUNT(*) FROM artists WHERE country_resolved = 0`)
}

func (s *Store) pendingMBIDs(ctx context.Context, q string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.query(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) countRows(ctx context.Context, q string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, q).Scan(&n)
	return n, err
}

// SaveRecordings stores a batch of recording metadata along with its artists and tags.
// requested lists every MBID asked for, so those the API returned nothing for are recorded
// as misses instead of being retried on the next run. The whole batch is one transaction,
// so an interrupt leaves the database consistent and only that batch is redone.
func (s *Store) SaveRecordings(ctx context.Context, requested []string, metas map[string]listenbrainz.RecordingMeta) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	recStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO recordings
			(mbid, name, length, first_release_date, first_release_year,
			 release_mbid, release_group_mbid, release_name, release_year, found)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(mbid) DO UPDATE SET
			name = excluded.name, length = excluded.length,
			first_release_date = excluded.first_release_date,
			first_release_year = excluded.first_release_year,
			release_mbid = excluded.release_mbid,
			release_group_mbid = excluded.release_group_mbid,
			release_name = excluded.release_name,
			release_year = excluded.release_year,
			found = excluded.found`)
	if err != nil {
		return err
	}
	defer recStmt.Close()

	credStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO recording_artists (recording_mbid, artist_mbid, position)
		VALUES (?, ?, ?)
		ON CONFLICT(recording_mbid, position) DO UPDATE SET artist_mbid = excluded.artist_mbid`)
	if err != nil {
		return err
	}
	defer credStmt.Close()

	for _, mbid := range requested {
		m, ok := metas[mbid]
		if !ok {
			if _, err := recStmt.ExecContext(ctx, mbid, "", 0, "", 0, "", "", "", 0, 0); err != nil {
				return err
			}
			continue
		}
		if _, err := recStmt.ExecContext(ctx, mbid, m.Name, m.Length, m.FirstReleaseDate,
			listenbrainz.ParseYear(m.FirstReleaseDate), m.ReleaseMBID, m.ReleaseGroupMBID,
			m.ReleaseName, m.ReleaseYear, 1); err != nil {
			return err
		}
		if err := saveTags(ctx, tx, TagRecording, mbid, m.RecordingTags); err != nil {
			return err
		}
		if m.ReleaseGroupMBID != "" {
			if err := saveTags(ctx, tx, TagReleaseGroup, m.ReleaseGroupMBID, m.ReleaseGroupTags); err != nil {
				return err
			}
		}
		for i, a := range m.Artists {
			if _, err := credStmt.ExecContext(ctx, mbid, a.MBID, i); err != nil {
				return err
			}
			if err := saveArtist(ctx, tx, a); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// SaveArtists stores artist metadata fetched directly, recording misses like SaveRecordings does.
func (s *Store) SaveArtists(ctx context.Context, requested []string, metas map[string]listenbrainz.ArtistMeta) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, mbid := range requested {
		m, ok := metas[mbid]
		if !ok {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO artists (mbid, found) VALUES (?, 0) ON CONFLICT(mbid) DO NOTHING`, mbid); err != nil {
				return err
			}
			continue
		}
		if err := saveArtist(ctx, tx, m); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// saveArtist writes artist metadata without touching country, which the MusicBrainz pass owns.
func saveArtist(ctx context.Context, tx *sql.Tx, a listenbrainz.ArtistMeta) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artists (mbid, name, area, type, gender, begin_year, end_year, found)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1)
		ON CONFLICT(mbid) DO UPDATE SET
			name = excluded.name, area = excluded.area, type = excluded.type,
			gender = excluded.gender, begin_year = excluded.begin_year,
			end_year = excluded.end_year, found = 1`,
		a.MBID, a.Name, a.Area, a.Type, a.Gender, a.BeginYear, a.EndYear); err != nil {
		return err
	}
	return saveTags(ctx, tx, TagArtist, a.MBID, a.Tags)
}

func saveTags(ctx context.Context, tx *sql.Tx, entityType, entityMBID string, tags []listenbrainz.Tag) error {
	if len(tags) == 0 {
		return nil
	}
	// Replace rather than merge so a re-enrich reflects tags that were since removed upstream.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM tags WHERE entity_type = ? AND entity_mbid = ?`, entityType, entityMBID); err != nil {
		return err
	}
	for _, t := range tags {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO tags (entity_type, entity_mbid, tag, genre_mbid, count)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(entity_type, entity_mbid, tag) DO UPDATE SET
				genre_mbid = excluded.genre_mbid, count = excluded.count`,
			entityType, entityMBID, strings.ToLower(t.Tag), t.GenreMBID, t.Count); err != nil {
			return err
		}
	}
	return nil
}

// SaveCountries records the outcome of a country pass. Every MBID in requested is marked
// resolved, including those with no country, so the pass converges instead of looping.
func (s *Store) SaveCountries(ctx context.Context, requested []string, countries map[string]string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx,
		`UPDATE artists SET country = ?, country_resolved = 1 WHERE mbid = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, mbid := range requested {
		if _, err := stmt.ExecContext(ctx, countries[mbid], mbid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ClearMetadata drops everything the enrich pass owns, leaving listens untouched.
func (s *Store) ClearMetadata(ctx context.Context) error {
	for _, table := range []string{"recordings", "artists", "recording_artists", "tags"} {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM `+table); err != nil {
			return err
		}
	}
	return nil
}
