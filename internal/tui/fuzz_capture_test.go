package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Bloopps/dockstack/internal/backup"
)

// FuzzCaptureFile : un fichier de capture quelconque (tronqué, corrompu,
// modifié à la main), au format JSON comme au format texte d'origine, ne fait
// paniquer ni la lecture, ni la vue des captures, ni le choix des stacks à
// relancer, et ne permet jamais de viser un dossier hors du dossier des stacks.
func FuzzCaptureFile(f *testing.F) {
	f.Add([]byte(`{"version":2,"created":"2026-09-30T10:00:00Z","stack_dir":"/srv","stacks":[{"name":"media/app","services":["web"]}]}`), false)
	f.Add([]byte("# Timestamp: 2026-09-30_10-00-00\n# Stack dir: /srv\nmedia/app\n../../etc\n"), true)
	f.Add([]byte(`{"stacks":[{"name":""},{"name":"/abs"},{"name":"a","services":[""]}]}`), false)
	f.Add([]byte(`{"version":2,"stacks":null,"label":"\u0000\u001b[31m"}`), false)
	f.Fuzz(func(t *testing.T, data []byte, legacy bool) {
		dir := t.TempDir()
		name := "capture.2026-09-30_10-00-00.json"
		if legacy {
			name = "active_stacks.2026-09-30_10-00-00"
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
		snaps, err := backup.List(dir)
		if err != nil {
			t.Fatal(err)
		}
		m := screenModel(8)
		m.width, m.height = 120, 40
		m.backups = snaps
		m.view = viewBackup
		_ = m.viewContent()
		for _, s := range snaps {
			_ = diffCapture(s.Items(), m.stacks)
			stacks, _ := resolveRestoreStacks(s.Items(), "/srv")
			for _, st := range stacks {
				if rel, err := filepath.Rel("/srv", st.Dir); err != nil || !filepath.IsLocal(rel) {
					t.Fatalf("relance hors du dossier des stacks : %q", st.Dir)
				}
			}
		}
		if len(snaps) > 0 {
			out, _ := m.Update(keyMsg("enter"))
			_ = out.(Model).viewContent()
		}
	})
}
