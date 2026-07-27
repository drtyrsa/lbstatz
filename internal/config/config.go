package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

const (
	configName = "lbstatz.toml"
	dbName     = "lbstatz.db"
)

type Config struct {
	Token    string `toml:"token"`
	Username string `toml:"username"`
	DBPath   string `toml:"db_path"`
}

// DefaultConfigPath returns the config path next to the running binary.
func DefaultConfigPath() (string, error) {
	dir, err := binaryDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, configName), nil
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("config file %s not found; create one with 'token', 'username' and optional 'db_path'", path)
		}
		return nil, err
	}

	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	if cfg.Username == "" {
		return nil, fmt.Errorf("config %s: 'username' is required", path)
	}

	if cfg.DBPath == "" {
		dir := filepath.Dir(path)
		cfg.DBPath = filepath.Join(dir, dbName)
	} else {
		cfg.DBPath = expandPath(cfg.DBPath)
	}

	return &cfg, nil
}

func expandPath(p string) string {
	if p == "~" || len(p) >= 2 && p[:2] == "~/" {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

func binaryDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		resolved = exe
	}
	return filepath.Dir(resolved), nil
}
