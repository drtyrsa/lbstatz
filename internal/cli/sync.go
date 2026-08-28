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
	var remap bool

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Download listens from ListenBrainz into the local database",
		Long: "Download listens into the local database, then fetch metadata for any new\n" +
			"recordings and artists so the genre, country and era stats stay current.\n" +
			"Both halves are resumable; interrupt with Ctrl-C and run sync again.\n\n" +
			"With --remap it also re-checks listens ListenBrainz could not identify when they\n" +
			"were downloaded, which is worth doing every so often as MusicBrainz grows.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// A from-scratch sync re-downloads every listen with its current mapping, so it
			// already does everything --remap would, and doing both would only repeat the work.
			if remap && fromScratch {
				return fmt.Errorf("--remap cannot be combined with --from-scratch")
			}

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

			counts, err := syncListens(cmd.Context(), client, st, fromScratch, prog.report)
			if err != nil && !errors.Is(err, context.Canceled) {
				prog.finish("")
				return err
			}

			bg := context.Background()
			stored, _ := st.Count(bg)
			remote, _ := client.ListenCount(bg)
			backfillDone, _ := st.BackfillComplete(bg)

			if errors.Is(err, context.Canceled) {
				prog.finish(fmt.Sprintf("Interrupted: %d new listens saved (%s). Run sync again to continue.", counts.Inserted, storedOf(stored, remote)))
				return nil
			}
			if !backfillDone && remote > stored {
				prog.finish(fmt.Sprintf("Stopped early: %d new listens saved (%s). Run sync again to continue.", counts.Inserted, storedOf(stored, remote)))
				return nil
			}
			prog.finish(fmt.Sprintf("Downloaded %d new listens%s (%s).",
				counts.Inserted, remappedSuffix(counts.Remapped), storedOf(stored, remote)))

			if remap {
				if err := runRemap(cmd.Context(), client, st); err != nil {
					if errors.Is(err, context.Canceled) {
						return nil
					}
					return err
				}
			}

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
	cmd.Flags().BoolVar(&remap, "remap", false, "also re-check listens ListenBrainz could not identify when they were downloaded")
	return cmd
}

func storedOf(stored, remote int64) string {
	if remote > 0 {
		return fmt.Sprintf("%d of %d stored", stored, remote)
	}
	return fmt.Sprintf("%d stored", stored)
}

// remappedSuffix mentions listens that gained MBIDs, which the top-up overlap fills in as a
// side effect of every sync, and says nothing when there were none.
func remappedSuffix(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(", remapped %d", n)
}

// runRemap drives the remap sweep and reports what it turned up. It is skipped while the
// history is still downloading: re-walking a range that isn't fully stored yet spends
// requests on listens the backfill is about to fetch with current mappings anyway.
func runRemap(ctx context.Context, client listenSource, st *store.Store) error {
	backfillDone, err := st.BackfillComplete(ctx)
	if err != nil {
		return err
	}
	if !backfillDone {
		fmt.Fprintln(os.Stderr, "Skipping remap: the history is still downloading. Run sync until it finishes, then remap.")
		return nil
	}

	before, err := st.UnmappedListenCount(ctx)
	if err != nil {
		return err
	}
	if before == 0 {
		fmt.Fprintln(os.Stderr, "Remap: every listen is already identified.")
		return nil
	}

	prog := newProgress(os.Stderr, "Remapping")
	counts, err := remapListens(ctx, client, st, prog.report)
	if err != nil && !errors.Is(err, context.Canceled) {
		prog.finish("")
		return err
	}

	// Whatever the sweep managed is committed, so report against the database either way —
	// including when ctx is the reason it stopped.
	left, _ := st.UnmappedListenCount(context.WithoutCancel(ctx))
	if errors.Is(err, context.Canceled) {
		prog.finish(fmt.Sprintf("Interrupted: identified %d listens, %d still unmatched. Run sync --remap again to continue.",
			before-left, left))
		return err
	}

	msg := fmt.Sprintf("Remapped %d of %d unidentified listens; %d still have no match.", before-left, before, left)
	if counts.Inserted > 0 {
		msg += fmt.Sprintf(" Also stored %d listens that were missing locally.", counts.Inserted)
	}
	prog.finish(msg)
	return nil
}

// syncListens brings the local database up to date in two phases. First it tops up any
// listens newer than what is stored. Then, unless a previous run already walked the whole
// history, it backfills older listens starting below the oldest stored one. Because the
// backfill cursor is the oldest stored timestamp, an interrupted sync resumes cleanly.
// The top-up page walks back until it meets listens already stored, so that overlap is
// re-fetched every run and any MBIDs it has gained since are filled in for free — which
// covers the common case, a listen ListenBrainz identifies an hour after it was scrobbled.
// Reaching further back than the overlap is what --remap is for.
func syncListens(ctx context.Context, client listenSource, st *store.Store, fromScratch bool, report func(done, total int64)) (store.UpsertResult, error) {
	var counts store.UpsertResult
	if fromScratch {
		if err := st.Clear(ctx); err != nil {
			return counts, err
		}
	}

	remoteTotal, _ := client.ListenCount(ctx)
	storedBefore, err := st.Count(ctx)
	if err != nil {
		return counts, err
	}

	progress := func() {
		if report != nil {
			report(storedBefore+int64(counts.Inserted), remoteTotal)
		}
	}
	progress()

	newest, err := st.MaxListenedAt(ctx)
	if err != nil {
		return counts, err
	}
	if newest > 0 {
		if _, err := walkBack(ctx, client, st, 0, newest, &counts, progress); err != nil {
			return counts, err
		}
	}

	backfillDone, err := st.BackfillComplete(ctx)
	if err != nil {
		return counts, err
	}
	if !backfillDone {
		oldest, err := st.MinListenedAt(ctx)
		if err != nil {
			return counts, err
		}
		// max_ts is exclusive; +1 re-requests the boundary second so listens sharing it aren't skipped.
		cursor := oldest
		if cursor > 0 {
			cursor++
		}
		reachedOldest, err := walkBack(ctx, client, st, cursor, 0, &counts, progress)
		if err != nil {
			return counts, err
		}
		if reachedOldest {
			if err := st.SetBackfillComplete(ctx, true); err != nil {
				return counts, err
			}
		}
	}
	return counts, nil
}

// remapListens re-fetches the stretches of history that still hold unidentified listens, so
// mappings ListenBrainz has made since those listens were downloaded get picked up.
//
// Rather than re-walking everything it jumps the cursor from one unidentified listen to the
// next, skipping stretches that are already fully mapped. A history whose gaps are clustered
// — one player that submitted poor metadata for a few months — costs a handful of requests,
// and one where they are scattered evenly costs no more than a full walk would.
func remapListens(ctx context.Context, client listenSource, st *store.Store, report func(done, total int64)) (store.UpsertResult, error) {
	var res store.UpsertResult

	total, err := st.UnmappedListenCount(ctx)
	if err != nil || total == 0 {
		return res, err
	}
	cursor, err := st.NewestUnmappedBefore(ctx, 0)
	if err != nil {
		return res, err
	}

	for cursor > 0 {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		// max_ts is exclusive, so aim one second above the listen the cursor is targeting.
		page, err := client.Page(ctx, cursor+1)
		if err != nil {
			return res, err
		}
		if len(page.Listens) == 0 {
			return res, nil
		}

		batch, err := st.UpsertListens(ctx, page.Listens)
		if err != nil {
			return res, err
		}
		res = res.Add(batch)

		if report != nil {
			left, err := st.UnmappedListenCount(ctx)
			if err != nil {
				return res, err
			}
			report(max(0, total-left), total)
		}

		if len(page.Listens) < listenbrainz.MaxItemsPerGet {
			return res, nil
		}
		// Resume strictly below the page just covered so the cursor always moves, whether or
		// not this page's listens turned out to be mappable.
		oldest := page.Listens[len(page.Listens)-1].ListenedAt
		cursor, err = st.NewestUnmappedBefore(ctx, oldest)
		if err != nil {
			return res, err
		}
	}
	return res, nil
}

// walkBack pages from maxTS (0 = most recent) toward older listens, storing each page.
// It stops at boundary (exclusive; 0 = no boundary) or when the history runs out, and
// reports whether it reached the oldest listen.
func walkBack(ctx context.Context, client listenSource, st *store.Store, maxTS, boundary int64, counts *store.UpsertResult, progress func()) (bool, error) {
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

		res, err := st.UpsertListens(ctx, batch)
		if err != nil {
			return false, err
		}
		*counts = counts.Add(res)
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
