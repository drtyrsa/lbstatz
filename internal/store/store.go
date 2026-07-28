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

CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
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

// UpsertListens inserts listens, ignoring ones already stored, and reports how many were new.
func (s *Store) UpsertListens(ctx context.Context, listens []listenbrainz.Listen) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR IGNORE INTO listens
			(listened_at, recording_msid, artist_name, track_name, release_name, artist_mbid, artist_mbids, recording_mbid, release_mbid)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	inserted := 0
	for _, l := range listens {
		res, err := stmt.ExecContext(ctx, l.ListenedAt, l.RecordingMSID, l.ArtistName, l.TrackName,
			l.ReleaseName, l.ArtistMBID, artistMBIDsKey(l.ArtistMBIDs), l.RecordingMBID, l.ReleaseMBID)
		if err != nil {
			return 0, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
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
