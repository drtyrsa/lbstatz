package cli

import (
	"fmt"

	"github.com/drtyrsa/lbstatz/internal/store"
	"github.com/spf13/cobra"
)

func newErasCmd(configPath *string) *cobra.Command {
	var ff filterFlags
	var trend bool
	var bucketName string

	cmd := &cobra.Command{
		Use:   "eras",
		Short: "Show how old the music you listen to is",
		Long: "Break your listens down by when the music was first released.\n\n" +
			"By default this is a histogram over release decades; --bucket year gives one row\n" +
			"per release year instead. With --trend it instead reports, for each year you\n" +
			"listened, the median release year and the median age of the music at the time you\n" +
			"played it. Age is the more honest of the two: a rising median release year happens\n" +
			"automatically as time passes, whereas a rising median age means you really did\n" +
			"drift toward older music.\n\n" +
			"To break the histogram down by when you listened, filter to one year at a time:\n" +
			"`eras --from 2024-01-01 --to 2024-12-31`.\n\n" +
			"Release dates come from `enrich`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			bucket, err := store.ParseBucket(bucketName)
			if err != nil {
				return err
			}
			// --bucket shapes the release axis, which --trend replaces entirely; accepting both
			// would silently ignore one of them.
			if trend && cmd.Flags().Changed("bucket") {
				return fmt.Errorf("--bucket cannot be combined with --trend")
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

			if trend {
				rows, cov, err := st.EraTrend(cmd.Context(), filter)
				if err != nil {
					return err
				}
				return printTrend(cmd.OutOrStdout(), rows, cov, ff.jsonOut)
			}

			rows, cov, err := st.Eras(cmd.Context(), filter, bucket)
			if err != nil {
				return err
			}
			return printEras(cmd.OutOrStdout(), rows, bucket, cov, ff.jsonOut)
		},
	}
	ff.bind(cmd)
	cmd.Flags().BoolVar(&trend, "trend", false, "show the trend per year of listening instead of a release histogram")
	cmd.Flags().StringVar(&bucketName, "bucket", "decade", "histogram granularity: decade or year")
	return cmd
}
