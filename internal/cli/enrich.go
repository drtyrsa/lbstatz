package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/drtyrsa/lbstatz/internal/listenbrainz"
	"github.com/drtyrsa/lbstatz/internal/musicbrainz"
	"github.com/drtyrsa/lbstatz/internal/store"
	"github.com/spf13/cobra"
)

type metadataSource interface {
	Recordings(ctx context.Context, mbids []string) (map[string]listenbrainz.RecordingMeta, error)
	Artists(ctx context.Context, mbids []string) (map[string]listenbrainz.ArtistMeta, error)
}

type countrySource interface {
	Countries(ctx context.Context, mbids []string) (map[string]string, error)
	Country(ctx context.Context, mbid string) (string, error)
}

type enrichStats struct {
	Recordings int
	Artists    int
	Countries  int
}

func (e enrichStats) String() string {
	return fmt.Sprintf("%d recordings, %d artists, %d countries", e.Recordings, e.Artists, e.Countries)
}

func (e enrichStats) empty() bool {
	return e.Recordings == 0 && e.Artists == 0 && e.Countries == 0
}

func newEnrichCmd(configPath *string) *cobra.Command {
	var refresh bool

	cmd := &cobra.Command{
		Use:   "enrich",
		Short: "Fetch release dates, countries and genres for the entities you listen to",
		Long: "Fetch metadata for the recordings and artists in your listen history.\n\n" +
			"`sync` runs this automatically, so you rarely need it directly. Use it on its own\n" +
			"to resume an interrupted pass, or with --refresh to re-pull metadata that has been\n" +
			"edited upstream since it was last fetched. Listens are never re-downloaded.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(*configPath)
			if err != nil {
				return err
			}
			st, err := openStore(cfg)
			if err != nil {
				return err
			}
			defer st.Close()

			if refresh {
				if err := st.ClearMetadata(cmd.Context()); err != nil {
					return err
				}
			}

			lb := listenbrainz.New(listenbrainz.DefaultBaseURL, cfg.Token, cfg.Username)
			mb := musicbrainz.New(musicbrainz.DefaultBaseURL)

			stats, err := enrichAll(cmd.Context(), lb, mb, st, os.Stderr)
			if err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
			if errors.Is(err, context.Canceled) {
				fmt.Fprintf(os.Stderr, "Interrupted: enriched %s. Run enrich again to continue.\n", stats)
				return nil
			}
			fmt.Fprintf(os.Stderr, "Done: enriched %s.\n", stats)
			return nil
		},
	}
	cmd.Flags().BoolVar(&refresh, "refresh", false, "discard stored metadata and fetch it again")
	return cmd
}

// enrichAll fills in metadata for every entity the listen history references but the
// database does not describe yet. Each phase is driven by what is still missing rather than
// by a cursor, so interrupting at any point loses at most the batch in flight and the next
// run picks up exactly where this one stopped.
func enrichAll(ctx context.Context, lb metadataSource, mb countrySource, st *store.Store, w io.Writer) (enrichStats, error) {
	var stats enrichStats

	n, err := enrichRecordings(ctx, lb, st, w)
	stats.Recordings = n
	if err != nil {
		return stats, err
	}

	n, err = enrichArtists(ctx, lb, st, w)
	stats.Artists = n
	if err != nil {
		return stats, err
	}

	n, err = enrichCountries(ctx, mb, st, w)
	stats.Countries = n
	return stats, err
}

func enrichRecordings(ctx context.Context, lb metadataSource, st *store.Store, w io.Writer) (int, error) {
	total, err := st.PendingRecordingCount(ctx)
	if err != nil || total == 0 {
		return 0, err
	}
	prog := newProgress(w, "Recordings")
	defer prog.clear()

	done := 0
	for {
		if err := ctx.Err(); err != nil {
			return done, err
		}
		batch, err := st.PendingRecordings(ctx, listenbrainz.MaxMetadataPerGet)
		if err != nil || len(batch) == 0 {
			return done, err
		}
		metas, err := lb.Recordings(ctx, batch)
		if err != nil {
			return done, err
		}
		if err := st.SaveRecordings(ctx, batch, metas); err != nil {
			return done, err
		}
		done += len(batch)
		prog.report(int64(done), total)
	}
}

func enrichArtists(ctx context.Context, lb metadataSource, st *store.Store, w io.Writer) (int, error) {
	total, err := st.PendingArtistCount(ctx)
	if err != nil || total == 0 {
		return 0, err
	}
	prog := newProgress(w, "Artists")
	defer prog.clear()

	done := 0
	for {
		if err := ctx.Err(); err != nil {
			return done, err
		}
		batch, err := st.PendingArtists(ctx, listenbrainz.MaxMetadataPerGet)
		if err != nil || len(batch) == 0 {
			return done, err
		}
		metas, err := lb.Artists(ctx, batch)
		if err != nil {
			return done, err
		}
		if err := st.SaveArtists(ctx, batch, metas); err != nil {
			return done, err
		}
		done += len(batch)
		prog.report(int64(done), total)
	}
}

// enrichCountries resolves ISO country codes via MusicBrainz. The batched search index is
// tried first, then artists it left blank are looked up individually, because the index
// reports no country for subdivision areas like England while a direct lookup resolves them.
func enrichCountries(ctx context.Context, mb countrySource, st *store.Store, w io.Writer) (int, error) {
	total, err := st.PendingCountryCount(ctx)
	if err != nil || total == 0 {
		return 0, err
	}
	prog := newProgress(w, "Countries")
	defer prog.clear()

	done := 0
	for {
		if err := ctx.Err(); err != nil {
			return done, err
		}
		batch, err := st.PendingCountries(ctx, musicbrainz.MaxSearchPerGet)
		if err != nil || len(batch) == 0 {
			return done, err
		}

		mbids := make([]string, 0, len(batch))
		for _, a := range batch {
			mbids = append(mbids, a.MBID)
		}
		found, err := mb.Countries(ctx, mbids)
		if err != nil {
			return done, err
		}

		for _, a := range batch {
			if found[a.MBID] != "" || a.Area == "" {
				continue
			}
			if err := ctx.Err(); err != nil {
				// Persist what this batch already resolved; ctx is cancelled, so use a fresh one.
				_ = st.SaveCountries(context.Background(), mbids, found)
				return done, err
			}
			code, err := mb.Country(ctx, a.MBID)
			if err != nil {
				return done, err
			}
			if code != "" {
				found[a.MBID] = code
			}
		}

		if err := st.SaveCountries(ctx, mbids, found); err != nil {
			return done, err
		}
		done += len(batch)
		prog.report(int64(done), total)
	}
}
