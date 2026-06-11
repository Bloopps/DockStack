package compose

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/compose-spec/compose-go/v2/loader"
	composetypes "github.com/compose-spec/compose-go/v2/types"
	composeapi "github.com/docker/compose/v5/pkg/api"
)

var skipDirs = map[string]bool{
	// VCS / deps
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	// Go workspace / module cache
	"go":  true,
	"pkg": true,
	// Caches / tooling
	".cache":      true,
	"snap":        true,
	"__pycache__": true,
	".npm":        true,
	".cargo":      true,
	".gradle":     true,
	// Systèmes de fichiers virtuels si stackDir est /
	"proc": true,
	"sys":  true,
	"dev":  true,
	"run":  true,
	"tmp":  true,
}

const composeFileTTL = 60 * time.Second

// maxScanDepth bounds how many directory levels below the stack root we descend
// when looking for compose files. Real stacks live at most a few levels deep
// (e.g. group/sub-stack/), while deeper hits are almost always compose files
// buried in source/test trees. This caps the startup scan: at depth 0 = the root
// itself, so 3 covers root/group/stack layouts without walking entire projects.
const maxScanDepth = 3

var (
	cacheMu         sync.Mutex
	cachedFiles     []string
	cachedFilesAt   time.Time
	cachedFilesRoot string
)

func InvalidateComposeCache() {
	cacheMu.Lock()
	cachedFilesAt = time.Time{}
	cacheMu.Unlock()
}

// PrewarmComposeCache populates the compose-file cache for root. It is meant to
// run concurrently with the Docker client init at startup so the filesystem walk
// overlaps the (slow) daemon handshake instead of running after it.
func PrewarmComposeCache(root string) {
	_, _ = findComposeFiles(root)
}

func findComposeFiles(root string) ([]string, error) {
	cacheMu.Lock()
	if root == cachedFilesRoot && time.Since(cachedFilesAt) < composeFileTTL {
		files := cachedFiles
		cacheMu.Unlock()
		return files, nil
	}
	cacheMu.Unlock()

	var files []string
	scanComposeDir(root, 0, &files)

	sort.Strings(files)
	cacheMu.Lock()
	cachedFiles = files
	cachedFilesAt = time.Now()
	cachedFilesRoot = root
	cacheMu.Unlock()
	return files, nil
}

// scanComposeDir walks dir depth-first collecting compose files, descending at
// most maxScanDepth levels below the root (depth 0). Unlike a full WalkDir it does
// not dive into deep project/source trees, which is what made the startup scan
// slow (~100ms over a home dir) for no benefit: stacks nested deeper than a few
// levels are almost always example/buried compose files, not real stack roots.
// Sub-stacks (e.g. a group dir whose children each hold a compose) are still
// found because we keep descending past a compose until the depth cap is hit.
func scanComposeDir(dir string, depth int, files *[]string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	// Une seule stack par répertoire : si plusieurs fichiers compose
	// coexistent (compose.yaml + docker-compose.yml…), on garde le plus
	// prioritaire selon l'ordre de docker compose, au lieu de créer une
	// stack dupliquée par fichier.
	best := ""
	bestRank := len(composeRank)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if r, ok := composeRank[e.Name()]; ok && r < bestRank {
			best, bestRank = e.Name(), r
		}
	}
	if best != "" {
		*files = append(*files, filepath.Join(dir, best))
	}
	if depth >= maxScanDepth {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if skipDirs[name] || strings.HasPrefix(name, ".") {
			continue
		}
		scanComposeDir(filepath.Join(dir, name), depth+1, files)
	}
}

// composeNamesByPriority ordonne les noms de fichiers compose par priorité
// décroissante (l'ordre de préférence de docker compose : compose.yaml
// d'abord, les noms docker-compose.* en compatibilité).
var composeNamesByPriority = []string{
	"compose.yaml",
	"compose.yml",
	"docker-compose.yml",
	"docker-compose.yaml",
}

var composeRank = func() map[string]int {
	m := make(map[string]int, len(composeNamesByPriority))
	for i, n := range composeNamesByPriority {
		m[n] = i
	}
	return m
}()

// FindComposeFile renvoie le fichier compose de dir selon l'ordre de priorité
// de docker compose, ou "" si le répertoire n'en contient aucun.
func FindComposeFile(dir string) string {
	for _, name := range composeNamesByPriority {
		if p := filepath.Join(dir, name); fileExists(p) {
			return p
		}
	}
	return ""
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func loadProject(stack Stack) (*composetypes.Project, error) {
	// Fallback project name: directory basename, normalized to what Docker accepts.
	// SetProjectName with imperativelySet=false lets a name: field in the compose
	// file still override this fallback.
	fallback := loader.NormalizeProjectName(filepath.Base(stack.Dir))
	if fallback == "" {
		fallback = "stack"
	}

	// Environnement d'interpolation comme la CLI : variables du process + le
	// .env du répertoire de la stack (que docker compose charge tout seul).
	// Sans lui, ${VAR} s'interpole en vide et produit des specs invalides
	// (ex. volume « :/var/lib/postgresql » chez immich).
	env := make(map[string]string)
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	if envFile := filepath.Join(stack.Dir, ".env"); fileExists(envFile) {
		if fileEnv, err := dotenv.GetEnvFromFile(env, []string{envFile}); err == nil {
			for k, v := range fileEnv {
				if _, exists := env[k]; !exists { // le process garde la priorité
					env[k] = v
				}
			}
		}
	}

	proj, err := loader.LoadWithContext(context.Background(), composetypes.ConfigDetails{
		WorkingDir:  stack.Dir,
		ConfigFiles: []composetypes.ConfigFile{{Filename: stack.ComposeFile}},
		Environment: env,
	}, func(o *loader.Options) {
		o.SkipValidation = true
		o.SetProjectName(fallback, false)
	})
	if err != nil {
		return nil, err
	}
	// Garde-fou : le nom ne doit jamais être vide
	if proj.Name == "" {
		proj.Name = filepath.Base(stack.Dir)
	}
	// Comme la CLI docker compose : élague réseaux/volumes/secrets non
	// référencés par les services. Sans ça, un réseau déclaré mais inutilisé
	// (ex. `default: {name: x}` à côté d'un `x: {external: true}`) est
	// quand même contrôlé au Up et échoue sur son label compose.network.
	proj = proj.WithoutUnnecessaryResources()
	// Set the compose labels that the compose v5 service normally adds via
	// postProcessProject (internal). Without this, containers created through
	// our loadProject+Up path lack project/service/working_dir labels.
	for name, svc := range proj.Services {
		if svc.CustomLabels == nil {
			svc.CustomLabels = composetypes.Labels{}
		}
		svc.CustomLabels[composeapi.ProjectLabel] = proj.Name
		svc.CustomLabels[composeapi.ServiceLabel] = name
		svc.CustomLabels[composeapi.WorkingDirLabel] = proj.WorkingDir
		svc.CustomLabels[composeapi.ConfigFilesLabel] = strings.Join(proj.ComposeFiles, ",")
		svc.CustomLabels[composeapi.OneoffLabel] = "False"
		proj.Services[name] = svc
	}
	return proj, nil
}
