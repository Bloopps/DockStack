package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Config struct {
	StackDir    string `json:"stack_dir"`
	MaxParallel int    `json:"max_parallel"`
}

func dir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".dockerstack")
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
