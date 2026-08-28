package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/drtyrsa/lbstatz/internal/listenbrainz"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS listens (
	listened_at    INTEGER NOT NULL,
	recording_msid TEXT NOT NULL DEFAULT '',
	artist_name    TEXT NOT NULL DEFAULT '',
	track_name     TEXT NOT NULL DEFAULT '',
	release_name   TEXT NOT NULL DEFAULT '',
	artist_mbid    TEXT NOT NULL DEFAULT '',
	artist_mbids   TEXT NOT NULL DEFAULT '',
	recording_mbid TEXT NOT NULL DEFAULT '',
	release_mbid   TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (listened_at, recording_msid)
);
CREATE INDEX IF NOT EXISTS idx_listens_listened_at ON listens(listened_at);
-- Partial index over the listens ListenBrainz has not identified, which the remap pass walks
-- newest-first. It covers only the unidentified rows, so it stays small as coverage improves.
CREATE INDEX IF NOT EXISTS idx_listens_unmapped ON listens(listened_at) WHERE recording_mbid = '';

CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

-- Entity metadata fetched from ListenBrainz/MusicBrainz, keyed by MBID rather than by
-- listen, so re-fetching is independent of the listen history.
CREATE TABLE IF NOT EXISTS recordings (
	mbid               TEXT PRIMARY KEY,
	name               TEXT NOT NULL DEFAULT '',
	length             INTEGER NOT NULL DEFAULT 0,
	first_release_date TEXT NOT NULL DEFAULT '',
	first_release_year INTEGER NOT NULL DEFAULT 0,
	release_mbid       TEXT NOT NULL DEFAULT '',
	release_group_mbid TEXT NOT NULL DEFAULT '',
	release_name       TEXT NOT NULL DEFAULT '',
	release_year       INTEGER NOT NULL DEFAULT 0,
	-- 0 marks an MBID the API has no metadata for, so enrich stops re-requesting it.
	found              INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS idx_recordings_year ON recordings(first_release_year);

CREATE TABLE IF NOT EXISTS artists (
	mbid       TEXT PRIMARY KEY,
	name       TEXT NOT NULL DEFAULT '',
	area       TEXT NOT NULL DEFAULT '',
	country    TEXT NOT NULL DEFAULT '',
	type       TEXT NOT NULL DEFAULT '',
	gender     TEXT NOT NULL DEFAULT '',
	begin_year INTEGER NOT NULL DEFAULT 0,
	end_year   INTEGER NOT NULL DEFAULT 0,
	found      INTEGER NOT NULL DEFAULT 1,
	-- 0 until the MusicBrainz country pass has looked at this artist, whatever it found.
	country_resolved INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS recording_artists (
	recording_mbid TEXT NOT NULL,
	artist_mbid    TEXT NOT NULL,
	position       INTEGER NOT NULL,
	PRIMARY KEY (recording_mbid, position)
);
CREATE INDEX IF NOT EXISTS idx_recording_artists_artist ON recording_artists(artist_mbid);

-- Tags are stored raw at every level so genre attribution stays a query-time decision.
CREATE TABLE IF NOT EXISTS tags (
	entity_type TEXT NOT NULL,
	entity_mbid TEXT NOT NULL,
	tag         TEXT NOT NULL,
	genre_mbid  TEXT NOT NULL DEFAULT '',
	count       INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (entity_type, entity_mbid, tag)
);
CREATE INDEX IF NOT EXISTS idx_tags_genre ON tags(entity_type, entity_mbid) WHERE genre_mbid <> '';
`

const metaBackfillComplete = "backfill_complete"

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// UpsertResult reports what a batch of listens changed: rows stored for the first time, and
// rows already held that gained MBIDs they were missing.
type UpsertResult struct {
	Inserted int
	Remapped int
}

// Add sums two results, for callers accumulating across pages.
func (r UpsertResult) Add(o UpsertResult) UpsertResult {
	return UpsertResult{Inserted: r.Inserted + o.Inserted, Remapped: r.Remapped + o.Remapped}
}

// UpsertListens stores listens, filling in MBIDs on the ones already held. ListenBrainz maps
// listens to MusicBrainz in the background and keeps improving those mappings, so a listen
// that arrived unidentified can gain MBIDs long after it was downloaded. Only empty columns
// are filled: a mapping that has since been withdrawn must never blank one already stored,
// and MBIDs the listen was submitted with are authoritative.
func (s *Store) UpsertListens(ctx context.Context, listens []listenbrainz.Listen) (UpsertResult, error) {
	var res UpsertResult
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()

	insert, err := tx.PrepareContext(ctx, `
		INSERT OR IGNORE INTO listens
			(listened_at, recording_msid, artist_name, track_name, release_name, artist_mbid, artist_mbids, recording_mbid, release_mbid)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return res, err
	}
	defer insert.Close()

	// The trailing condition holds the update to rows it would really change, so an unchanged
	// listen is not rewritten and RowsAffected counts genuine remappings rather than passes.
	fill, err := tx.PrepareContext(ctx, `
		UPDATE listens SET
			artist_mbid    = COALESCE(NULLIF(?1, ''), artist_mbid),
			artist_mbids   = COALESCE(NULLIF(?2, ''), artist_mbids),
			recording_mbid = COALESCE(NULLIF(?3, ''), recording_mbid),
			release_mbid   = COALESCE(NULLIF(?4, ''), release_mbid)
		WHERE listened_at = ?5 AND recording_msid = ?6
		  AND (   (artist_mbid    = '' AND ?1 <> '')
		       OR (artist_mbids   = '' AND ?2 <> '')
		       OR (recording_mbid = '' AND ?3 <> '')
		       OR (release_mbid   = '' AND ?4 <> ''))`)
	if err != nil {
		return res, err
	}
	defer fill.Close()

	for _, l := range listens {
		mbids := artistMBIDsKey(l.ArtistMBIDs)
		r, err := insert.ExecContext(ctx, l.ListenedAt, l.RecordingMSID, l.ArtistName, l.TrackName,
			l.ReleaseName, l.ArtistMBID, mbids, l.RecordingMBID, l.ReleaseMBID)
		if err != nil {
			return res, err
		}
		if n, _ := r.RowsAffected(); n > 0 {
			res.Inserted++
			continue
		}
		// Already stored, so only the identity columns can have moved on since.
		r, err = fill.ExecContext(ctx, l.ArtistMBID, mbids, l.RecordingMBID, l.ReleaseMBID,
			l.ListenedAt, l.RecordingMSID)
		if err != nil {
			return res, err
		}
		if n, _ := r.RowsAffected(); n > 0 {
			res.Remapped++
		}
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}
	return res, nil
}

// UnmappedListenCount reports how many listens ListenBrainz has no recording MBID for. They
// are dead weight for every metadata stat, since those are all keyed off that MBID.
func (s *Store) UnmappedListenCount(ctx context.Context) (int64, error) {
	return s.countRows(ctx, `SELECT COUNT(*) FROM listens WHERE recording_mbid = ''`)
}

// NewestUnmappedBefore returns the timestamp of the newest listen with no recording MBID
// older than before (0 = no bound), or 0 when there is none left to look at.
func (s *Store) NewestUnmappedBefore(ctx context.Context, before int64) (int64, error) {
	var ts sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT MAX(listened_at) FROM listens
		WHERE recording_mbid = '' AND (?1 = 0 OR listened_at < ?1)`, before).Scan(&ts)
	return ts.Int64, err
}

func (s *Store) Count(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM listens`).Scan(&n)
	return n, err
}

// MaxListenedAt returns the newest stored listen timestamp, or 0 when empty.
func (s *Store) MaxListenedAt(ctx context.Context) (int64, error) {
	return s.boundaryTS(ctx, "MAX")
}

// MinListenedAt returns the oldest stored listen timestamp, or 0 when empty.
func (s *Store) MinListenedAt(ctx context.Context) (int64, error) {
	return s.boundaryTS(ctx, "MIN")
}

func (s *Store) boundaryTS(ctx context.Context, agg string) (int64, error) {
	var ts sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT `+agg+`(listened_at) FROM listens`).Scan(&ts); err != nil {
		return 0, err
	}
	return ts.Int64, nil
}

// BackfillComplete reports whether the entire history has been downloaded at least once.
func (s *Store) BackfillComplete(ctx context.Context) (bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, metaBackfillComplete).Scan(&v)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return v == "1", err
}

func (s *Store) SetBackfillComplete(ctx context.Context, done bool) error {
	v := "0"
	if done {
		v = "1"
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		metaBackfillComplete, v)
	return err
}

func (s *Store) Clear(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM listens`); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM meta`)
	return err
}

// artistMBIDsKey encodes a listen's artist MBIDs as an order-independent, comma-delimited
// membership key (e.g. ",id1,id2,") so credits merge by identity and single MBIDs match with instr.
func artistMBIDsKey(mbids []string) string {
	clean := make([]string, 0, len(mbids))
	for _, m := range mbids {
		if m != "" {
			clean = append(clean, m)
		}
	}
	if len(clean) == 0 {
		return ""
	}
	sort.Strings(clean)
	return "," + strings.Join(clean, ",") + ","
}

func (s *Store) query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	return rows, nil
}
