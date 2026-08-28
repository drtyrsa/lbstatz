package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/drtyrsa/lbstatz/internal/listenbrainz"
	"github.com/drtyrsa/lbstatz/internal/musicbrainz"
	"github.com/drtyrsa/lbstatz/internal/store"
	"github.com/spf13/cobra"
)

type listenSource interface {
	Page(ctx context.Context, maxTS int64) (*listenbrainz.Page, error)
	ListenCount(ctx context.Context) (int64, error)
}

func newSyncCmd(configPath *string) *cobra.Command {
	var fromScratch bool
	var noEnrich bool

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Download listens from ListenBrainz into the local database",
		Long: "Download listens into the local database, then fetch metadata for any new\n" +
			"recordings and artists so the genre, country and era stats stay current.\n" +
			"Both halves are resumable; interrupt with Ctrl-C and run sync again.",
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

			client := listenbrainz.New(listenbrainz.DefaultBaseURL, cfg.Token, cfg.Username)
			prog := newProgress(os.Stderr, "Downloaded")

			n, err := syncListens(cmd.Context(), client, st, fromScratch, prog.report)
			if err != nil && !errors.Is(err, context.Canceled) {
				prog.finish("")
				return err
			}

			bg := context.Background()
			stored, _ := st.Count(bg)
			remote, _ := client.ListenCount(bg)
			backfillDone, _ := st.BackfillComplete(bg)

			if errors.Is(err, context.Canceled) {
				prog.finish(fmt.Sprintf("Interrupted: %d new listens saved (%s). Run sync again to continue.", n, storedOf(stored, remote)))
				return nil
			}
			if !backfillDone && remote > stored {
				prog.finish(fmt.Sprintf("Stopped early: %d new listens saved (%s). Run sync again to continue.", n, storedOf(stored, remote)))
				return nil
			}
			prog.finish(fmt.Sprintf("Downloaded %d new listens (%s).", n, storedOf(stored, remote)))

			if noEnrich {
				return nil
			}

			// Metadata is keyed by MBID, so this only ever fetches entities the database does
			// not already describe — re-running sync costs nothing once it has caught up.
			mb := musicbrainz.New(musicbrainz.DefaultBaseURL)
			stats, err := enrichAll(cmd.Context(), client, mb, st, os.Stderr)
			if err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
			if errors.Is(err, context.Canceled) {
				fmt.Fprintf(os.Stderr, "Interrupted: enriched %s. Run sync again to continue.\n", stats)
				return nil
			}
			if stats.empty() {
				fmt.Fprintln(os.Stderr, "Done: metadata already up to date.")
			} else {
				fmt.Fprintf(os.Stderr, "Done: enriched %s.\n", stats)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fromScratch, "from-scratch", false, "re-download the entire history instead of only new listens")
	cmd.Flags().BoolVar(&noEnrich, "no-enrich", false, "download listens only, skipping the metadata pass")
	return cmd
}

func storedOf(stored, remote int64) string {
	if remote > 0 {
		return fmt.Sprintf("%d of %d stored", stored, remote)
	}
	return fmt.Sprintf("%d stored", stored)
}

// syncListens brings the local database up to date in two phases. First it tops up any
// listens newer than what is stored. Then, unless a previous run already walked the whole
// history, it backfills older listens starting below the oldest stored one. Because the
// backfill cursor is the oldest stored timestamp, an interrupted sync resumes cleanly.
func syncListens(ctx context.Context, client listenSource, st *store.Store, fromScratch bool, report func(done, total int64)) (int, error) {
	if fromScratch {
		if err := st.Clear(ctx); err != nil {
			return 0, err
		}
	}

	remoteTotal, _ := client.ListenCount(ctx)
	storedBefore, err := st.Count(ctx)
	if err != nil {
		return 0, err
	}

	newCount := 0
	progress := func() {
		if report != nil {
			report(storedBefore+int64(newCount), remoteTotal)
		}
	}
	progress()

	newest, err := st.MaxListenedAt(ctx)
	if err != nil {
		return newCount, err
	}
	if newest > 0 {
		if _, err := walkBack(ctx, client, st, 0, newest, &newCount, progress); err != nil {
			return newCount, err
		}
	}

	backfillDone, err := st.BackfillComplete(ctx)
	if err != nil {
		return newCount, err
	}
	if !backfillDone {
		oldest, err := st.MinListenedAt(ctx)
		if err != nil {
			return newCount, err
		}
		// max_ts is exclusive; +1 re-requests the boundary second so listens sharing it aren't skipped.
		cursor := oldest
		if cursor > 0 {
			cursor++
		}
		reachedOldest, err := walkBack(ctx, client, st, cursor, 0, &newCount, progress)
		if err != nil {
			return newCount, err
		}
		if reachedOldest {
			if err := st.SetBackfillComplete(ctx, true); err != nil {
				return newCount, err
			}
		}
	}
	return newCount, nil
}

// walkBack pages from maxTS (0 = most recent) toward older listens, storing each page.
// It stops at boundary (exclusive; 0 = no boundary) or when the history runs out, and
// reports whether it reached the oldest listen.
func walkBack(ctx context.Context, client listenSource, st *store.Store, maxTS, boundary int64, newCount *int, progress func()) (bool, error) {
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}

		page, err := client.Page(ctx, maxTS)
		if err != nil {
			return false, err
		}
		if len(page.Listens) == 0 {
			return true, nil
		}

		batch := page.Listens
		reachedBoundary := false
		if boundary > 0 {
			kept := batch[:0]
			for _, l := range page.Listens {
				if l.ListenedAt >= boundary {
					kept = append(kept, l)
				} else {
					reachedBoundary = true
				}
			}
			batch = kept
		}

		inserted, err := st.UpsertListens(ctx, batch)
		if err != nil {
			return false, err
		}
		*newCount += inserted
		progress()

		if reachedBoundary {
			return false, nil
		}
		if len(page.Listens) < listenbrainz.MaxItemsPerGet {
			return true, nil
		}

		// max_ts is exclusive, so the cursor goes one second above the oldest listen on this
		// page rather than onto it: stepping onto it would drop any other listen sharing that
		// exact second, which is how a page boundary silently loses a listen for good. The
		// duplicates that re-requesting the second brings back cost nothing, the upsert
		// already dedupes them.
		oldest := page.Listens[len(page.Listens)-1].ListenedAt
		next := oldest + 1
		if maxTS > 0 && next >= maxTS {
			// A whole page inside a single second. The API offers no way to page within one,
			// so asking again would return this same page forever; step past it instead and
			// accept that the rest of that second is out of reach.
			next = oldest
		}
		maxTS = next
	}
}
