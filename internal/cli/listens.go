package cli

import (
	"github.com/spf13/cobra"
)

func newListensCmd(configPath *string) *cobra.Command {
	var ff filterFlags

	cmd := &cobra.Command{
		Use:   "listens",
		Short: "List individual listens, most recent first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
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

			rows, err := st.Listens(cmd.Context(), filter, ff.limit)
			if err != nil {
				return err
			}
			return printListens(cmd.OutOrStdout(), rows, ff.jsonOut)
		},
	}
	ff.bind(cmd)
	return cmd
}
