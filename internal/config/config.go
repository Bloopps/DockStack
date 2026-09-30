package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultMaxParallel : nombre de stacks traitées en même temps par une action
// groupée quand la config n'en fixe pas (ou fixe une valeur absurde).
const DefaultMaxParallel = 4

type Config struct {
	StackDir    string `json:"stack_dir"`
	MaxParallel int    `json:"max_parallel"`
	// ShowAll affiche aussi les stacks qui ne tournent pas (arrêtées ou jamais
	// déployées). Par défaut la liste ne montre que ce qui tourne : sur un
	// home, le reste est surtout des dépôts de dev et d'exemples. Remplace
	// l'ancien show_inactive (non déployées seulement), ignoré à la lecture
	// pour que le nouveau défaut s'applique.
	ShowAll bool `json:"show_all,omitempty"`
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
	cfg := &Config{MaxParallel: DefaultMaxParallel}

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
	// 0 ou négatif (valeur écrite à 0 par erreur, ou saisie à la main) : on
	// retombe sur le défaut, sinon les actions groupées passaient une stack à
	// la fois sans que rien ne l'explique.
	if cfg.MaxParallel <= 0 {
		cfg.MaxParallel = DefaultMaxParallel
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
