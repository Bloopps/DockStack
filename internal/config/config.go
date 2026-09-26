package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	StackDir    string `json:"stack_dir"`
	MaxParallel int    `json:"max_parallel"`
	// ShowInactive affiche les stacks jamais déployées (masquées par défaut :
	// sur un home, ce sont surtout des dépôts de dev et d'exemples).
	ShowInactive bool `json:"show_inactive,omitempty"`
	// Exclude liste des chemins relatifs au dossier des stacks à ignorer
	// (le chemin lui-même et tout ce qui est en dessous).
	Exclude []string `json:"exclude,omitempty"`
}

// IsExcluded dit si la stack de chemin relatif name tombe sous une exclusion.
func (c *Config) IsExcluded(name string) bool {
	for _, e := range c.Exclude {
		if name == e || strings.HasPrefix(name, e+"/") {
			return true
		}
	}
	return false
}

func dir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".dockstack")
}

func path() string {
	return filepath.Join(dir(), "config.json")
}

func Load() (*Config, error) {
	cfg := &Config{MaxParallel: 4}

	data, err := os.ReadFile(path())
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) Save() error {
	if err := os.MkdirAll(dir(), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path(), data, 0o644)
}

func (c *Config) ConfigDir() string {
	return dir()
}
