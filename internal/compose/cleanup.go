package compose

// Nettoyage : bilan de l'espace utilisé par Docker (les chiffres de
// « docker system df ») et éléments supprimables, classés par risque. Rien
// n'est supprimé ici : l'analyse prépare la page « Nettoyage » de la TUI.

import (
	"context"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/distribution/reference"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/build"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"
	mobyclient "github.com/moby/moby/client"
)

// CleanupKind : catégorie d'un élément supprimable.
type CleanupKind int

const (
	// CleanBuildCache : cache de build inutilisé (un seul élément agrégé).
	CleanBuildCache CleanupKind = iota
	// CleanDanglingImage : image sans étiquette (<none>) et sans conteneur.
	CleanDanglingImage
	// CleanUnusedNetwork : réseau sans conteneur, qu'aucune stack ne déclare
	// externe.
	CleanUnusedNetwork
	// CleanUnusedImage : image étiquetée sans conteneur, qu'aucun compose du
	// dossier ne cite.
	CleanUnusedImage
	// CleanStackImage : image sans conteneur citée par un compose du dossier
	// (stack arrêtée ou jamais déployée) : un up devra la retélécharger.
	CleanStackImage
	// CleanOrphanVolume : volume sans conteneur dont le projet compose n'a
	// plus de stack dans le dossier.
	CleanOrphanVolume
	// CleanAnonymousVolume : volume anonyme sans conteneur.
	CleanAnonymousVolume
)

// CleanupRisk : ce que coûte la suppression.
type CleanupRisk int

const (
	// RiskNone : rien de perdu (cache, images pendantes, réseaux recréés par
	// compose).
	RiskNone CleanupRisk = iota
	// RiskRedownload : l'image devra être retéléchargée si elle resert.
	RiskRedownload
	// RiskData : des données sont effacées.
	RiskData
)

// Risk renvoie le risque de la catégorie.
func (k CleanupKind) Risk() CleanupRisk {
	switch k {
	case CleanUnusedImage, CleanStackImage:
		return RiskRedownload
	case CleanOrphanVolume, CleanAnonymousVolume:
		return RiskData
	}
	return RiskNone
}

// CleanupItem : un élément supprimable.
type CleanupItem struct {
	Kind CleanupKind
	// ID : ce qu'il faut supprimer (image, réseau ou volume). Vide pour le
	// cache de build.
	ID string
	// Name : étiquettes d'une image, nom d'un réseau ou d'un volume.
	Name string
	// Refs : étiquettes d'une image (à retirer une à une pour la supprimer
	// sans forcer).
	Refs []string
	// Size : octets libérés en supprimant cet élément seul, -1 si inconnu.
	// Pour une image : ses couches que nulle autre image n'utilise (voir
	// CleanupReport.Layers pour une sélection de plusieurs images).
	Size int64
	// Approx : taille estimée (image dont les couches n'ont pas pu être
	// détaillées).
	Approx bool
	// Count : nombre d'entrées regroupées (cache de build), 1 sinon.
	Count int
	// Stacks : stacks qui citent l'image (CleanStackImage) ou projet du
	// volume orphelin.
	Stacks  []string
	Created time.Time
}

// KeptVolume : volume sans conteneur que le nettoyage ne propose pas.
type KeptVolume struct {
	Name  string
	Stack string // stack présente sur le disque, vide si le volume est sans étiquette
	Size  int64  // -1 si inconnue
}

// StoppedContainer : conteneur arrêté et sa stack (projet compose).
type StoppedContainer struct {
	Name, Project string
}

// KeptNetwork : réseau inutilisé conservé, et les stacks qui le déclarent.
type KeptNetwork struct {
	Name   string
	Stacks []string
}

// DFRow : une ligne de « docker system df ».
type DFRow struct {
	Count, Active     int64
	Size, Reclaimable int64
}

// CleanupReport : bilan et éléments supprimables.
type CleanupReport struct {
	At                                  time.Time
	Images, Containers, Volumes, Builds DFRow
	Items                               []CleanupItem
	// KeptStackVolumes : volumes sans conteneur d'une stack présente sur le
	// disque (arrêtée) : ses données. Seul son Remove les supprime.
	KeptStackVolumes []KeptVolume
	// KeptUnlabeledVolumes : volumes nommés sans projet compose (créés à la
	// main ou par un autre outil) : conservés.
	KeptUnlabeledVolumes []KeptVolume
	// StoppedContainers : conteneurs arrêtés, ce que « docker container
	// prune » supprimerait.
	StoppedContainers []StoppedContainer
	// KeptExternalNetworks : réseaux inutilisés que des stacks déclarent
	// externes ; « docker network prune » les supprimerait quand même.
	KeptExternalNetworks []KeptNetwork
	// LoadErrors : stacks dont le compose n'a pas pu être lu ; leurs images
	// ne sont pas reconnues comme citées.
	LoadErrors map[string]error
	// Layers : couches de toutes les images, pour calculer exactement
	// l'espace libéré par une sélection d'images (Layers.Freed).
	Layers *LayerIndex
	// debugImages : images telles que lues au démon (diagnostic des tests).
	debugImages []image.Summary
}

// stackRefs : ce que les stacks du dossier citent.
type stackRefs struct {
	projects     map[string]string   // nom de projet (minuscules) → stack
	images       map[string][]string // référence d'image normalisée → stacks
	externalNets map[string][]string // nom de réseau externe → stacks
	errors       map[string]error
}

// cleanupInput : ce que l'analyse lit au démon.
type cleanupInput struct {
	du     mobyclient.DiskUsageResult
	ctrs   []container.Summary
	nets   []network.Summary
	refs   stackRefs
	layers *LayerIndex // nil : tailles propres du démon
}

// AnalyzeCleanup lit l'occupation au démon et classe ce qui peut être
// supprimé. stackDir : le dossier des stacks ; un volume ou une image cité
// par l'une d'elles n'est jamais pris pour un déchet. Coûteux (le démon
// mesure les volumes) : à appeler à l'ouverture de la page seulement.
func (c *Client) AnalyzeCleanup(ctx context.Context, stackDir string) (CleanupReport, error) {
	refs, err := collectStackRefs(stackDir)
	if err != nil {
		return CleanupReport{}, err
	}
	cli := c.cli.Client()
	du, err := cli.DiskUsage(ctx, mobyclient.DiskUsageOptions{
		Containers: true, Images: true, BuildCache: true, Volumes: true, Verbose: true,
	})
	if err != nil {
		return CleanupReport{}, err
	}
	ctrs, err := cli.ContainerList(ctx, mobyclient.ContainerListOptions{All: true})
	if err != nil {
		return CleanupReport{}, err
	}
	nets, err := cli.NetworkList(ctx, mobyclient.NetworkListOptions{})
	if err != nil {
		return CleanupReport{}, err
	}
	layers := c.layerIndex(ctx, du.Images.Items)
	if err := ctx.Err(); err != nil {
		return CleanupReport{}, err
	}
	rep := classifyCleanup(cleanupInput{du: du, ctrs: ctrs.Items, nets: nets.Items, refs: refs, layers: layers})
	rep.At = time.Now()
	return rep, nil
}

// collectStackRefs charge chaque stack du dossier comme le ferait compose
// (.env, override) et relève son nom de projet, ses images et ses réseaux
// externes. Une stack illisible garde au moins son nom de projet par défaut
// (dossier normalisé), pour que ses volumes ne passent pas pour orphelins.
func collectStackRefs(stackDir string) (stackRefs, error) {
	refs := stackRefs{
		projects:     map[string]string{},
		images:       map[string][]string{},
		externalNets: map[string][]string{},
		errors:       map[string]error{},
	}
	files, err := findComposeFiles(stackDir)
	if err != nil {
		return refs, err
	}
	for _, f := range files {
		dir := filepath.Dir(f)
		name, _ := filepath.Rel(stackDir, dir)
		st := Stack{Name: name, Dir: dir, ComposeFile: f}
		base := strings.ToLower(filepath.Base(dir))
		refs.projects[base] = name
		if n := loader.NormalizeProjectName(base); n != "" {
			refs.projects[n] = name
		}
		proj, err := loadProject(st)
		if err != nil {
			refs.errors[name] = err
			continue
		}
		refs.projects[strings.ToLower(proj.Name)] = name
		for _, svc := range proj.Services {
			imgs := append([]string{api.GetImageNameOrDefault(svc, proj.Name)}, api.GetDependentImages(svc, proj.Name)...)
			for _, img := range imgs {
				k := normalizeImageRef(img)
				if !containsStr(refs.images[k], name) {
					refs.images[k] = append(refs.images[k], name)
				}
			}
		}
		for key, n := range proj.Networks {
			if !bool(n.External) {
				continue
			}
			nn := n.Name
			if nn == "" {
				nn = key
			}
			refs.externalNets[nn] = append(refs.externalNets[nn], name)
		}
	}
	return refs, nil
}

// normalizeImageRef ramène une référence d'image à sa forme courte
// (« nginx » → « nginx:latest », « docker.io/library/nginx » → idem), celle
// des étiquettes renvoyées par le démon.
func normalizeImageRef(s string) string {
	named, err := reference.ParseNormalizedNamed(s)
	if err != nil {
		return s
	}
	if _, digested := named.(reference.Digested); digested {
		return reference.FamiliarString(named)
	}
	return reference.FamiliarString(reference.TagNameOnly(named))
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// predefinedNetworks : réseaux du démon, jamais supprimables.
var predefinedNetworks = map[string]bool{"bridge": true, "host": true, "none": true, "docker_gwbridge": true}

// classifyCleanup classe les éléments sans toucher au démon (testable sans
// Docker).
func classifyCleanup(in cleanupInput) CleanupReport {
	du := in.du
	rep := CleanupReport{
		Images:      DFRow{du.Images.TotalCount, du.Images.ActiveCount, du.Images.TotalSize, du.Images.Reclaimable},
		Containers:  DFRow{du.Containers.TotalCount, du.Containers.ActiveCount, du.Containers.TotalSize, du.Containers.Reclaimable},
		Volumes:     DFRow{du.Volumes.TotalCount, du.Volumes.ActiveCount, du.Volumes.TotalSize, du.Volumes.Reclaimable},
		Builds:      DFRow{du.BuildCache.TotalCount, du.BuildCache.ActiveCount, du.BuildCache.TotalSize, du.BuildCache.Reclaimable},
		LoadErrors:  in.refs.errors,
		Layers:      in.layers,
		debugImages: du.Images.Items,
	}

	// Cache de build : un seul élément, la part libérable selon le démon.
	if n := reclaimableCacheRecords(du.BuildCache.Items); n > 0 || du.BuildCache.Reclaimable > 0 {
		rep.Items = append(rep.Items, CleanupItem{Kind: CleanBuildCache, Name: "cache de build",
			Size: du.BuildCache.Reclaimable, Count: n})
	}

	// Images utilisées par un conteneur (même arrêté) : jamais proposées.
	usedImages := map[string]bool{}
	for _, c := range in.ctrs {
		usedImages[c.ImageID] = true
		if c.State != container.StateRunning && c.State != container.StatePaused && c.State != container.StateRestarting {
			name := ""
			if len(c.Names) > 0 {
				name = strings.TrimPrefix(c.Names[0], "/")
			}
			rep.StoppedContainers = append(rep.StoppedContainers, StoppedContainer{Name: name, Project: c.Labels[api.ProjectLabel]})
		}
	}
	// Une image qui sert de base à une autre (ancien constructeur) part avec
	// elle : ni pendante ni proposée seule.
	parents := map[string]bool{}
	for _, img := range du.Images.Items {
		if img.ParentID != "" {
			parents[img.ParentID] = true
		}
	}
	for _, img := range du.Images.Items {
		if img.Containers != 0 || usedImages[img.ID] || parents[img.ID] {
			continue // utilisée, base d'une autre, ou nombre inconnu (-1) : on garde
		}
		rep.Items = append(rep.Items, classifyImage(img, in.refs, in.layers))
	}

	// Réseaux.
	usedNets := map[string]bool{}
	for _, c := range in.ctrs {
		if c.NetworkSettings == nil {
			continue
		}
		for name, ep := range c.NetworkSettings.Networks {
			usedNets[name] = true
			if ep != nil && ep.NetworkID != "" {
				usedNets[ep.NetworkID] = true
			}
		}
	}
	for _, n := range in.nets {
		if predefinedNetworks[n.Name] || n.Scope != "local" || n.Ingress || n.ConfigOnly ||
			usedNets[n.ID] || usedNets[n.Name] {
			continue
		}
		if stacks := in.refs.externalNets[n.Name]; len(stacks) > 0 {
			rep.KeptExternalNetworks = append(rep.KeptExternalNetworks, KeptNetwork{Name: n.Name, Stacks: stacks})
			continue
		}
		rep.Items = append(rep.Items, CleanupItem{Kind: CleanUnusedNetwork, ID: n.ID, Name: n.Name,
			Size: -1, Count: 1, Created: n.Created})
	}

	// Volumes sans conteneur.
	for _, v := range du.Volumes.Items {
		if v.UsageData == nil || v.UsageData.RefCount != 0 {
			continue // utilisé, ou inconnu : on garde
		}
		size := v.UsageData.Size
		project := strings.ToLower(v.Labels[api.ProjectLabel])
		switch {
		case isAnonymousVolume(v):
			rep.Items = append(rep.Items, CleanupItem{Kind: CleanAnonymousVolume, ID: v.Name, Name: v.Name,
				Size: size, Count: 1, Created: volumeCreated(v)})
		case project == "":
			rep.KeptUnlabeledVolumes = append(rep.KeptUnlabeledVolumes, KeptVolume{Name: v.Name, Size: size})
		case in.refs.projects[project] != "":
			rep.KeptStackVolumes = append(rep.KeptStackVolumes, KeptVolume{Name: v.Name, Stack: in.refs.projects[project], Size: size})
		default:
			rep.Items = append(rep.Items, CleanupItem{Kind: CleanOrphanVolume, ID: v.Name, Name: v.Name,
				Size: size, Count: 1, Stacks: []string{project}, Created: volumeCreated(v)})
		}
	}

	sort.SliceStable(rep.Items, func(i, j int) bool {
		a, b := rep.Items[i], rep.Items[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Size != b.Size {
			return a.Size > b.Size
		}
		return a.Name < b.Name
	})
	sort.Slice(rep.KeptStackVolumes, func(i, j int) bool { return rep.KeptStackVolumes[i].Name < rep.KeptStackVolumes[j].Name })
	sort.Slice(rep.KeptUnlabeledVolumes, func(i, j int) bool {
		return rep.KeptUnlabeledVolumes[i].Name < rep.KeptUnlabeledVolumes[j].Name
	})
	return rep
}

// classifyImage : image sans conteneur → pendante, citée par une stack, ou
// libre.
func classifyImage(img image.Summary, refs stackRefs, layers *LayerIndex) CleanupItem {
	size := img.Size
	if img.SharedSize > 0 && img.SharedSize <= size {
		size -= img.SharedSize
	}
	approx := layers != nil && !layers.Exact(img.ID)
	if layers != nil && !approx {
		size, _ = layers.Freed([]string{img.ID})
	}
	it := CleanupItem{ID: img.ID, Size: size, Count: 1, Approx: approx, Created: time.Unix(img.Created, 0)}
	var tags []string
	for _, t := range img.RepoTags {
		if t != "<none>:<none>" {
			tags = append(tags, t)
		}
	}
	if len(tags) == 0 {
		it.Kind = CleanDanglingImage
		it.Name = "<none>"
		if len(img.RepoDigests) > 0 {
			it.Name = strings.SplitN(img.RepoDigests[0], "@", 2)[0] + " <none>"
		}
		// Origine, quand l'image la porte : ancien build d'un service compose
		// (étiquettes posées par compose), sinon titre OCI.
		if p, svc := img.Labels[api.ProjectLabel], img.Labels[api.ServiceLabel]; p != "" {
			it.Name = "ancien build " + p
			if svc != "" {
				it.Name += "/" + svc
			}
		} else if title := img.Labels["org.opencontainers.image.title"]; title != "" {
			it.Name += " · " + title
		}
		return it
	}
	it.Name = strings.Join(tags, ", ")
	it.Refs = tags
	var stacks []string
	for _, r := range append(append([]string{}, tags...), img.RepoDigests...) {
		for _, s := range refs.images[normalizeImageRef(r)] {
			if !containsStr(stacks, s) {
				stacks = append(stacks, s)
			}
		}
	}
	if len(stacks) > 0 {
		sort.Strings(stacks)
		it.Kind, it.Stacks = CleanStackImage, stacks
	} else {
		it.Kind = CleanUnusedImage
	}
	return it
}

// reclaimableCacheRecords : entrées du cache de build ni utilisées ni
// partagées.
func reclaimableCacheRecords(items []build.CacheRecord) int {
	n := 0
	for _, r := range items {
		if !r.InUse && !r.Shared {
			n++
		}
	}
	return n
}

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// isAnonymousVolume : volume créé sans nom (étiquette posée par le démon
// depuis Docker 23, ou nom en 64 caractères hexadécimaux sans projet).
func isAnonymousVolume(v volume.Volume) bool {
	if _, ok := v.Labels["com.docker.volume.anonymous"]; ok {
		return true
	}
	return hex64.MatchString(v.Name) && v.Labels[api.ProjectLabel] == ""
}

func volumeCreated(v volume.Volume) time.Time {
	t, _ := time.Parse(time.RFC3339, v.CreatedAt)
	return t
}
