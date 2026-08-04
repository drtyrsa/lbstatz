package cli

import (
	"os"
	"path/filepath"

	"github.com/drtyrsa/lbstatz/internal/config"
	"github.com/drtyrsa/lbstatz/internal/store"
	"github.com/spf13/cobra"
)

func NewRootCmd() *cobra.Command {
	var configPath string

	root := &cobra.Command{
		Use:           "lbstatz",
		Short:         "Local statistics for your ListenBrainz listens",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVarP(&configPath, "config", "c", "", "config file path (default: lbstatz.toml next to the binary)")

	root.AddCommand(
		newSyncCmd(&configPath),
		newEnrichCmd(&configPath),
		newTopCmd(&configPath),
		newErasCmd(&configPath),
		newListensCmd(&configPath),
	)
	return root
}

func loadConfig(path string) (*config.Config, error) {
	if path == "" {
		p, err := config.DefaultConfigPath()
		if err != nil {
			return nil, err
		}
		path = p
	}
	return config.Load(path)
}

func openStore(cfg *config.Config) (*store.Store, error) {
	if dir := filepath.Dir(cfg.DBPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	return store.Open(cfg.DBPath)
}
