package compose

import (
	"context"
	"slices"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/containerd/platforms"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/docker/compose/v5/pkg/compose"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	mobyclient "github.com/moby/moby/client"
	"github.com/moby/moby/client/pkg/versions"
)

// Recreation décrit un service en marche qu'un « up » recréerait (donc
// interromprait brièvement) et pourquoi.
type Recreation struct {
	Service string
	Reason  string // « config modifiée », « image mise à jour », « suit … », « orphelin … »
}

// PlanUp prévoit quels services EN MARCHE un up recréerait, en reproduisant le
// critère de compose (RecreateDiverged) : hash de configuration du service
// différent de celui du conteneur, image locale plus récente que celle du
// conteneur (après un pull), ou parent de namespace/volumes recréé (cascade).
// Les orphelins en marche (label projet perdu, cf. watchtower) comptent aussi :
// le up les supprime puis les recrée. C'est une prévision : elle sert à avertir
// avant une confirmation, sans jamais bloquer l'action.
func (c *Client) PlanUp(ctx context.Context, stack Stack) ([]Recreation, error) {
	proj, _, err := loadScopedProjectReport(&stack)
	if err != nil {
		return nil, err
	}
	// Tous les conteneurs du projet (arrêtés compris, one-off exclus), comme la
	// vue observée de compose : les références service:X se résolvent contre
	// eux, y compris pour un parent hors du projet réduit.
	f := make(mobyclient.Filters).Add("label", api.ProjectLabel+"="+proj.Name)
	res, err := c.cli.Client().ContainerList(ctx, mobyclient.ContainerListOptions{All: true, Filters: f})
	if err != nil {
		return nil, err
	}
	byService := map[string][]container.Summary{}
	for _, ctr := range res.Items {
		if ctr.Labels[api.OneoffLabel] == "True" {
			continue
		}
		name := ctr.Labels[api.ServiceLabel]
		byService[name] = append(byService[name], ctr)
	}
	for _, ctrs := range byService {
		sort.Slice(ctrs, func(i, j int) bool { return canonicalName(ctrs[i]) < canonicalName(ctrs[j]) })
	}

	names := make([]string, 0, len(proj.Services))
	for n := range proj.Services {
		names = append(names, n)
	}
	sort.Strings(names)

	reasons := map[string]string{}
	imageID := map[string]string{} // cache (ref d'image, plateforme) → empreinte locale
	withManifests := c.manifestsSupported(ctx)
	att := &attachCache{nets: map[string]string{}, vols: map[string]bool{}}
	for _, name := range names {
		sv := proj.Services[name]
		for _, ctr := range byService[name] {
			if ctr.State != container.StateRunning || reasons[name] != "" {
				continue
			}
			// Compose persiste le hash du service APRÈS résolution des
			// références (network_mode: service:X → container:<id>…) : hacher la
			// forme brute signalerait à tort « config modifiée » à chaque up.
			if want, err := compose.ServiceHash(resolveRefs(sv, byService)); err == nil {
				if have := ctr.Labels[api.ConfigHashLabel]; have != "" && have != want {
					reasons[name] = "config modifiée"
					continue
				}
			}
			if have := ctr.Labels[api.ImageDigestLabel]; have != "" && sv.Image != "" {
				key := sv.Image + "|" + sv.Platform
				id, cached := imageID[key]
				if !cached {
					id = c.imageContentDigest(ctx, sv.Image, sv.Platform, withManifests)
					imageID[key] = id
				}
				if id != "" && id != have {
					reasons[name] = "image mise à jour"
					continue
				}
			}
			// Réseau ou volume attendu absent du conteneur (déconnecté à la
			// main, réseau recréé…) : compose recrée aussi dans ce cas.
			if why := c.attachmentMismatch(ctx, proj, sv, ctr, att); why != "" {
				reasons[name] = why
			}
		}
	}

	// Cascade : un service qui partage le namespace (network_mode/ipc/pid) ou
	// les volumes (volumes_from) d'un parent recréé est recréé avec lui.
	for changed := true; changed; {
		changed = false
		for _, name := range names {
			if reasons[name] != "" || !anyRunning(byService[name]) {
				continue
			}
			if parent := recreatedParent(proj.Services[name], reasons); parent != "" {
				reasons[name] = "suit la recréation de " + parent
				changed = true
			}
		}
	}

	// Orphelins en marche : compose ne les voit pas, le up se heurte à leur nom,
	// les supprime puis les recrée (ré-adoption) — une coupure à annoncer.
	for svc := range c.runningOrphans(ctx, proj) {
		if reasons[svc] == "" {
			reasons[svc] = "orphelin (label projet perdu), sera recréé"
		}
	}

	out := make([]Recreation, 0, len(reasons))
	for n, r := range reasons {
		out = append(out, Recreation{Service: n, Reason: r})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out, nil
}

// attachCache mémorise, pour une prévision, l'id des réseaux (nom → id, ""
// si absent) et l'existence des volumes (nom → existe).
type attachCache struct {
	nets map[string]string
	vols map[string]bool
}

// attachmentMismatch reproduit hasNetworkMismatch et hasVolumeMismatch de
// compose (internes) pour un conteneur en marche : un réseau du service qui
// existe mais auquel le conteneur n'est pas connecté, ou un volume nommé qui
// existe mais n'est pas monté, entraîne une recréation. Un réseau ou volume
// encore inexistant (ou un réseau swarm) n'est pas jugé, comme compose.
func (c *Client) attachmentMismatch(ctx context.Context, proj *types.Project, sv types.ServiceConfig, ctr container.Summary, att *attachCache) string {
	api := c.cli.Client()
	for _, key := range sortedKeys(sv.Networks) {
		name := proj.Networks[key].Name
		if name == "" {
			continue
		}
		id, seen := att.nets[name]
		if !seen {
			if res, err := api.NetworkInspect(ctx, name, mobyclient.NetworkInspectOptions{}); err == nil && res.Network.Scope != "swarm" {
				id = res.Network.ID
			}
			att.nets[name] = id
		}
		if id == "" {
			continue
		}
		connected := false
		if ctr.NetworkSettings != nil {
			for _, ep := range ctr.NetworkSettings.Networks {
				if ep != nil && ep.NetworkID == id {
					connected = true
					break
				}
			}
		}
		if !connected {
			return "réseau " + key + " déconnecté"
		}
	}
	for _, vol := range sv.Volumes {
		if vol.Type != types.VolumeTypeVolume || vol.Source == "" {
			continue
		}
		name := proj.Volumes[vol.Source].Name
		if name == "" {
			continue
		}
		exists, seen := att.vols[name]
		if !seen {
			_, err := api.VolumeInspect(ctx, name, mobyclient.VolumeInspectOptions{})
			exists = err == nil
			att.vols[name] = exists
		}
		if !exists {
			continue
		}
		mounted := false
		for _, m := range ctr.Mounts {
			if m.Type == "volume" && m.Name == name {
				mounted = true
				break
			}
		}
		if !mounted {
			return "volume " + vol.Source + " non monté"
		}
	}
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// manifestsSupported : le démon sait-il détailler les manifestes d'une image
// (API ≥ 1.48, comme compose) ? En cas d'erreur, non : on retombe sur l'id.
func (c *Client) manifestsSupported(ctx context.Context) bool {
	api := c.cli.Client()
	if _, err := api.Ping(ctx, mobyclient.PingOptions{NegotiateAPIVersion: true}); err != nil {
		return false
	}
	return versions.GreaterThanOrEqualTo(api.ClientVersion(), "1.48")
}

// imageContentDigest calcule l'empreinte d'image que compose inscrit dans le
// label com.docker.compose.image (localContentDigest, interne à compose) :
// avec le stockage containerd, c'est le manifeste « image » de la plateforme
// visée, PAS l'id de l'image (un index qui couvre aussi les attestations).
// Comparer l'id annonçait « image mise à jour » sur des conteneurs à jour.
// "" si l'image est introuvable (pas de conclusion).
func (c *Client) imageContentDigest(ctx context.Context, ref, platform string, withManifests bool) string {
	var opts []mobyclient.ImageInspectOption
	if withManifests {
		opts = append(opts, mobyclient.ImageInspectWithManifests(true))
	}
	ins, err := c.cli.Client().ImageInspect(ctx, ref, opts...)
	if err != nil {
		return ""
	}
	var matcher platforms.Matcher = platforms.Default()
	if platform != "" {
		p, err := platforms.Parse(platform)
		if err != nil {
			return ""
		}
		matcher = platforms.NewMatcher(p)
	}
	return contentDigest(ins.InspectResponse, matcher)
}

// contentDigest reproduit localContentDigest/matchLocalManifest de compose :
// manifeste « image » disponible correspondant à la plateforme, sinon l'unique
// manifeste disponible, sinon l'id de l'image.
func contentDigest(ins image.InspectResponse, matcher platforms.Matcher) string {
	var available []image.ManifestSummary
	for _, m := range ins.Manifests {
		if m.Kind == image.ManifestKindImage && m.Available {
			available = append(available, m)
		}
	}
	for _, m := range available {
		if m.ImageData != nil && matcher.Match(m.ImageData.Platform) {
			return m.ID
		}
	}
	if len(available) == 1 {
		return available[0].ID
	}
	return ins.ID
}

// canonicalName : nom principal d'un conteneur, comme compose
// (getCanonicalContainerName) pour choisir le même conteneur de référence.
func canonicalName(c container.Summary) string {
	for _, n := range c.Names {
		if strings.LastIndex(n, "/") == 0 {
			return n[1:]
		}
	}
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	return c.ID
}

func anyRunning(ctrs []container.Summary) bool {
	for _, c := range ctrs {
		if c.State == container.StateRunning {
			return true
		}
	}
	return false
}

// serviceRef renvoie X pour un mode « service:X », "" sinon.
func serviceRef(mode string) string {
	if strings.HasPrefix(mode, types.NetworkModeServicePrefix) {
		return mode[len(types.NetworkModeServicePrefix):]
	}
	return ""
}

// resolveRefs reproduit resolveServiceReferences de compose (interne) :
// volumes_from puis network_mode/ipc/pid pointant vers un service sont
// remplacés par l'id du premier conteneur de ce service. Comme compose, une
// référence introuvable arrête la résolution (la forme brute est alors hachée).
func resolveRefs(sv types.ServiceConfig, byService map[string][]container.Summary) types.ServiceConfig {
	sv.VolumesFrom = slices.Clone(sv.VolumesFrom)
	for i, vol := range sv.VolumesFrom {
		spec := strings.Split(vol, ":")
		if spec[0] == "container" && len(spec) > 1 {
			sv.VolumesFrom[i] = spec[1]
			continue
		}
		deps := byService[spec[0]]
		if len(deps) == 0 {
			return sv
		}
		sv.VolumesFrom[i] = deps[0].ID
	}
	for _, mode := range []*string{&sv.NetworkMode, &sv.Ipc, &sv.Pid} {
		if name := serviceRef(*mode); name != "" {
			deps := byService[name]
			if len(deps) == 0 {
				return sv
			}
			*mode = types.ContainerPrefix + deps[0].ID
		}
	}
	return sv
}

// recreatedParent renvoie le parent (namespace ou volumes_from) de sv qui sera
// recréé, "" sinon (cf. parentNamespaceRecreated de compose).
func recreatedParent(sv types.ServiceConfig, reasons map[string]string) string {
	for _, mode := range []string{sv.NetworkMode, sv.Ipc, sv.Pid} {
		if name := serviceRef(mode); name != "" && reasons[name] != "" {
			return name
		}
	}
	for _, vol := range sv.VolumesFrom {
		if strings.HasPrefix(vol, types.ContainerPrefix) {
			continue
		}
		if name, _, _ := strings.Cut(vol, ":"); reasons[name] != "" {
			return name
		}
	}
	return ""
}

// runningOrphans renvoie les services du projet dont un conteneur orphelin (cf.
// isOrphanCandidate) est en marche. Erreur de listing : aucun (prévision).
func (c *Client) runningOrphans(ctx context.Context, proj *types.Project) map[string]bool {
	cand := projectContainerNames(proj)
	out := map[string]bool{}
	if len(cand) == 0 {
		return out
	}
	res, err := c.cli.Client().ContainerList(ctx, mobyclient.ContainerListOptions{})
	if err != nil {
		return out
	}
	for _, ctr := range res.Items {
		if !isOrphanCandidate(ctr.Labels) {
			continue
		}
		if svc, ok := matchByNames(cand, ctr.Names); ok {
			out[svc] = true
		}
	}
	return out
}
