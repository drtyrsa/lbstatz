package cli

import (
	"github.com/drtyrsa/lbstatz/internal/musicbrainz"
	"github.com/drtyrsa/lbstatz/internal/store"
	"github.com/spf13/cobra"
)

func newTopCmd(configPath *string) *cobra.Command {
	var ff filterFlags
	var minVotes int

	cmd := &cobra.Command{
		Use:   "top {artists|albums|tracks|genres|countries}",
		Short: "Show your most played artists, albums, tracks, genres or countries",
		Long: "Show your most played artists, albums, tracks, genres or countries.\n\n" +
			"Filters compose: e.g. `top tracks --artist Radiohead --album \"OK Computer\"`\n" +
			"lists the top tracks of that album. Names and MBIDs are both accepted.\n\n" +
			"Genres and countries are built from `enrich` metadata, and report what share of\n" +
			"the listens in range they could account for.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			entity, err := store.ParseEntity(args[0])
			if err != nil {
				return err
			}
			filter, err := ff.toFilter()
			if err != nil {
				return err
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

			switch entity {
			case store.Genres:
				rows, cov, err := st.TopGenres(cmd.Context(), filter, minVotes, ff.limit)
				if err != nil {
					return err
				}
				return printTopWithCoverage(cmd.OutOrStdout(), entity, rows, cov, ff.jsonOut)

			case store.Countries:
				rows, cov, err := st.TopCountries(cmd.Context(), filter, ff.limit)
				if err != nil {
					return err
				}
				for i := range rows {
					rows[i].Name = musicbrainz.CountryName(rows[i].Code)
				}
				return printTopWithCoverage(cmd.OutOrStdout(), entity, rows, cov, ff.jsonOut)
			}

			rows, err := st.Top(cmd.Context(), entity, filter, ff.limit)
			if err != nil {
				return err
			}
			return printTop(cmd.OutOrStdout(), entity, rows, ff.jsonOut)
		},
	}
	ff.bind(cmd)
	cmd.Flags().IntVar(&minVotes, "min-votes", 1,
		"ignore genre tags with fewer than this many MusicBrainz votes")
	return cmd
}
