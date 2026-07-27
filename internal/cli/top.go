package cli

import (
	"github.com/drtyrsa/lbstatz/internal/store"
	"github.com/spf13/cobra"
)

func newTopCmd(configPath *string) *cobra.Command {
	var ff filterFlags

	cmd := &cobra.Command{
		Use:   "top {artists|albums|tracks}",
		Short: "Show your most played artists, albums or tracks",
		Long: "Show your most played artists, albums or tracks.\n\n" +
			"Filters compose: e.g. `top tracks --artist Radiohead --album \"OK Computer\"`\n" +
			"lists the top tracks of that album. Names and MBIDs are both accepted.",
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

			rows, err := st.Top(cmd.Context(), entity, filter, ff.limit)
			if err != nil {
				return err
			}
			return printTop(cmd.OutOrStdout(), entity, rows, ff.jsonOut)
		},
	}
	ff.bind(cmd)
	return cmd
}
