package config

import (
	"encoding/json"
	"fmt"
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
		// Le chemin dans l'erreur : sans lui, l'utilisateur ne sait pas quel
		// fichier corriger ou supprimer pour pouvoir redémarrer.
		return nil, fmt.Errorf("%s illisible (corriger ou supprimer le fichier) : %w", path(), err)
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
	// Écriture atomique (fichier temporaire puis renommage) : un arrêt en
	// pleine écriture laissait un JSON tronqué, et Load refuse alors de
	// démarrer.
	tmp, err := os.CreateTemp(dir(), ".config-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path()); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

func (c *Config) ConfigDir() string {
	return dir()
}
