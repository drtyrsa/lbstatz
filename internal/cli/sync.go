package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/drtyrsa/lbstatz/internal/listenbrainz"
	"github.com/drtyrsa/lbstatz/internal/store"
	"github.com/spf13/cobra"
)

type listenSource interface {
	Page(ctx context.Context, maxTS int64) (*listenbrainz.Page, error)
	ListenCount(ctx context.Context) (int64, error)
}

func newSyncCmd(configPath *string) *cobra.Command {
	var fromScratch bool

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Download listens from ListenBrainz into the local database",
		Args:  cobra.NoArgs,
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

			total, _ := st.Count(context.Background())
			if errors.Is(err, context.Canceled) {
				prog.finish(fmt.Sprintf("Interrupted: %d new listens saved (total %d).", n, total))
				return nil
			}
			prog.finish(fmt.Sprintf("Done: %d new listens (total %d).", n, total))
			return nil
		},
	}
	cmd.Flags().BoolVar(&fromScratch, "from-scratch", false, "re-download the entire history instead of only new listens")
	return cmd
}

// syncListens pages backwards through the user's history. A full sync walks to the
// oldest listen; an incremental sync stops once it reaches listens already stored.
func syncListens(ctx context.Context, client listenSource, st *store.Store, fromScratch bool, report func(done, total int64)) (int, error) {
	if fromScratch {
		if err := st.Clear(ctx); err != nil {
			return 0, err
		}
	}

	boundary, err := st.MaxListenedAt(ctx)
	if err != nil {
		return 0, err
	}
	localBefore, err := st.Count(ctx)
	if err != nil {
		return 0, err
	}

	remoteTotal, _ := client.ListenCount(ctx)
	target := remoteTotal
	if !fromScratch {
		target = remoteTotal - localBefore
		if target < 0 {
			target = 0
		}
	}

	newCount := 0
	var maxTS int64
	for {
		if err := ctx.Err(); err != nil {
			return newCount, err
		}

		page, err := client.Page(ctx, maxTS)
		if err != nil {
			return newCount, err
		}
		if len(page.Listens) == 0 {
			break
		}

		batch := page.Listens
		reachedBoundary := false
		if boundary > 0 {
			kept := batch[:0]
			for _, l := range page.Listens {
				if l.ListenedAt > boundary {
					kept = append(kept, l)
				} else {
					reachedBoundary = true
				}
			}
			batch = kept
		}

		inserted, err := st.UpsertListens(ctx, batch)
		if err != nil {
			return newCount, err
		}
		newCount += inserted
		if report != nil {
			report(int64(newCount), target)
		}

		if reachedBoundary || len(page.Listens) < listenbrainz.MaxItemsPerGet {
			break
		}
		maxTS = page.Listens[len(page.Listens)-1].ListenedAt
	}
	return newCount, nil
}
