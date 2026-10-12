package fakedocker

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// LiveLock sérialise les tests d'intégration qui touchent au vrai démon,
// d'un paquet à l'autre : « go test ./... » lance les paquets en parallèle,
// et le nettoyage des objets jetables zzdsprobe* d'un paquet supprimait ceux
// qu'un autre venait de créer. Le verrou est rendu à la fin du test.
func LiveLock(t testing.TB) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(os.TempDir(), "dockstack-integration.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	})
}
