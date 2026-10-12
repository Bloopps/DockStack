package compose

// Espace exact libéré par la suppression d'un ensemble d'images. Une image
// est une pile de couches, partagées entre images : supprimer A seule ne
// libère que ses couches propres, supprimer A et B libère en plus les
// couches qu'elles sont seules à partager. Le démon ne donne la taille que
// par image ; on la reconstitue couche par couche à partir de l'historique.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"sync"

	"github.com/moby/moby/api/types/image"
	mobyclient "github.com/moby/moby/client"
)

// LayerIndex : couches de chaque image et leur taille.
type LayerIndex struct {
	chains map[string][]string // image → couches (identifiants de chaîne, ou lot)
	size   map[string]int64    // couche → octets
	users  map[string][]string // couche → images qui l'utilisent
	// approx : images dont le détail par couche est incertain ; leur part
	// est estimée par leur taille propre (Size - SharedSize).
	approx   map[string]bool
	fallback map[string]int64
	// Stockage d'images containerd (défaut des installations neuves de
	// Docker 29) : la taille d'une image compte, en plus de ses couches
	// décompressées (l'historique), les blobs compressés d'où elles
	// viennent, supprimés avec elle. unpacked et content : ces deux parts
	// par image, connues quand toutes ses couches le sont. contentStore :
	// au moins une image en porte, l'espace libéré n'est alors qu'estimé.
	unpacked     map[string]int64
	content      map[string]int64
	contentStore bool
}

// emptyInstr : instructions qui ne créent jamais de couche, quel que soit le
// constructeur (« #(nop) ENV … » de l'ancien, « ENV … » de buildkit).
var emptyInstr = regexp.MustCompile(`(?i)^(/bin/sh -c )?(#\(nop\)\s*)?(ENV|LABEL|CMD|ENTRYPOINT|EXPOSE|USER|ARG|STOPSIGNAL|HEALTHCHECK|SHELL|ONBUILD|VOLUME|MAINTAINER)\b`)

// alignLayerSizes associe une taille à chaque couche (de la plus ancienne à
// la plus récente). L'historique (le plus récent d'abord) a une entrée par
// instruction, de taille nulle pour celles sans couche, mais aussi pour une
// couche réellement vide : on écarte les instructions qui ne créent jamais
// de couche, puis, à défaut, les entrées de taille nulle. ok=false si aucune
// des deux lectures ne donne le bon nombre de couches.
func alignLayerSizes(layers int, hist []image.HistoryResponseItem) ([]int64, bool) {
	var byInstr, nonZero []int64
	for i := len(hist) - 1; i >= 0; i-- {
		e := hist[i]
		if e.Size > 0 {
			byInstr = append(byInstr, e.Size)
			nonZero = append(nonZero, e.Size)
		} else if !emptyInstr.MatchString(strings.TrimSpace(e.CreatedBy)) {
			byInstr = append(byInstr, 0)
		}
	}
	switch {
	case len(byInstr) == layers:
		return byInstr, true
	case len(nonZero) == layers:
		return nonZero, true
	}
	return nil, false
}

// chainIDs : identifiant de chaque couche dans sa pile (celui du magasin de
// couches de Docker) : la même couche posée sur une autre base est une autre
// couche.
func chainIDs(diffIDs []string) []string {
	out := make([]string, len(diffIDs))
	for i, d := range diffIDs {
		if i == 0 {
			out[i] = d
			continue
		}
		sum := sha256.Sum256([]byte(out[i-1] + " " + d))
		out[i] = "sha256:" + hex.EncodeToString(sum[:])
	}
	return out
}

// layerInfo : ce que le démon dit d'une image.
type layerInfo struct {
	size, shared int64
	diffIDs      []string
	hist         []image.HistoryResponseItem
	err          error
}

// buildLayerIndex reconstitue les couches. Contrôles : une couche partagée
// doit avoir la même taille dans toutes les images ; sinon elle devient
// inconnue. Les couches inconnues d'une image, si aucune autre image ne les
// utilise, sont regroupées en un lot dont la taille est exacte (taille de
// l'image moins ses couches connues). Sinon l'image est marquée approchée.
func buildLayerIndex(infos map[string]layerInfo) *LayerIndex {
	ix := &LayerIndex{
		chains: map[string][]string{}, size: map[string]int64{}, users: map[string][]string{},
		approx: map[string]bool{}, fallback: map[string]int64{},
		unpacked: map[string]int64{}, content: map[string]int64{},
	}
	conflict := map[string]bool{}
	for id, in := range infos {
		ix.fallback[id] = in.size
		if in.shared > 0 && in.shared <= in.size {
			ix.fallback[id] = in.size - in.shared
		}
		if in.err != nil {
			ix.approx[id] = true
			continue
		}
		chains := chainIDs(in.diffIDs)
		ix.chains[id] = chains
		for _, ch := range chains {
			ix.users[ch] = append(ix.users[ch], id)
		}
		sizes, ok := alignLayerSizes(len(chains), in.hist)
		if !ok {
			continue
		}
		for i, ch := range chains {
			if s, seen := ix.size[ch]; seen && s != sizes[i] {
				conflict[ch] = true
			}
			ix.size[ch] = sizes[i]
		}
	}
	for ch := range conflict {
		delete(ix.size, ch)
	}
	var lots []string
	for id, chains := range ix.chains {
		var known int64
		var unknown []string
		for _, ch := range chains {
			if s, ok := ix.size[ch]; ok {
				known += s
			} else {
				unknown = append(unknown, ch)
			}
		}
		if len(unknown) == 0 {
			ix.unpacked[id] = known
			// Au-delà de 2 % : marge contre un écart d'arrondi du démon, loin
			// sous la part des blobs compressés (souvent 30 à 100 %).
			if extra := infos[id].size - known; extra > 0 && extra > known/50 {
				ix.content[id] = extra
				ix.contentStore = true
			}
			continue
		}
		lot := infos[id].size - known
		alone := lot >= 0
		for _, ch := range unknown {
			if len(ix.users[ch]) != 1 {
				alone = false
			}
		}
		if !alone {
			ix.approx[id] = true
			continue
		}
		// Les couches inconnues ne servent qu'à cette image : un seul lot.
		key := "lot:" + id
		kept := chains[:0:0]
		for _, ch := range chains {
			if _, ok := ix.size[ch]; ok {
				kept = append(kept, ch)
			} else {
				delete(ix.users, ch)
			}
		}
		ix.chains[id] = append(kept, key)
		ix.size[key] = lot
		ix.users[key] = []string{id}
		lots = append(lots, id)
	}
	// Avec des blobs compressés, le lot d'une image (sa taille moins ses
	// couches connues) contient aussi le contenu compressé des couches
	// qu'elle partage : sa part n'est plus exacte.
	if ix.contentStore {
		for _, id := range lots {
			ix.approx[id] = true
		}
	}
	return ix
}

// Freed renvoie l'espace libéré par la suppression des images ids : les
// couches dont toutes les images utilisatrices sont supprimées. exact=false
// si une image de l'ensemble n'a pas pu être détaillée (sa taille propre est
// alors comptée), ou avec le stockage containerd : les blobs compressés des
// couches libérées sont alors estimés au taux de compression moyen des
// images supprimées (la taille de chaque blob n'est pas exposée par l'API).
func (ix *LayerIndex) Freed(ids []string) (bytes int64, exact bool) {
	if ix == nil {
		return 0, false
	}
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	exact = true
	seen := map[string]bool{}
	var layers, content, unpacked int64
	defer func() {
		if ix.contentStore && layers > 0 {
			exact = false
			if unpacked > 0 {
				bytes += int64(float64(layers) * float64(content) / float64(unpacked))
			}
		}
	}()
	for _, id := range ids {
		if ix.approx[id] {
			bytes += ix.fallback[id]
			exact = false
			continue
		}
		content += ix.content[id]
		unpacked += ix.unpacked[id]
		for _, ch := range ix.chains[id] {
			if seen[ch] {
				continue
			}
			seen[ch] = true
			all := true
			for _, u := range ix.users[ch] {
				if !set[u] {
					all = false
					break
				}
			}
			if all {
				bytes += ix.size[ch]
				layers += ix.size[ch]
			}
		}
	}
	return bytes, exact
}

// Exact dit si l'image a été détaillée couche par couche.
func (ix *LayerIndex) Exact(id string) bool {
	return ix != nil && !ix.approx[id] && ix.chains[id] != nil
}

// layerCache : couches et historique déjà lus, par identifiant d'image.
type layerCache struct {
	mu    sync.Mutex
	infos map[string]layerInfo
}

// layerIndex construit l'index des couches des images imgs. Seules les
// images absentes du cache sont lues au démon (deux appels chacune) ; les
// images disparues en sortent. Une lecture en échec n'est pas gardée.
func (c *Client) layerIndex(ctx context.Context, imgs []image.Summary) *LayerIndex {
	c.layers.mu.Lock()
	defer c.layers.mu.Unlock()
	if c.layers.infos == nil {
		c.layers.infos = map[string]layerInfo{}
	}
	var missing []image.Summary
	present := make(map[string]bool, len(imgs))
	for _, img := range imgs {
		present[img.ID] = true
		if _, ok := c.layers.infos[img.ID]; !ok {
			missing = append(missing, img)
		}
	}
	for id := range c.layers.infos {
		if !present[id] {
			delete(c.layers.infos, id)
		}
	}
	for id, in := range fetchLayerInfos(ctx, c.cli.Client(), missing) {
		if in.err == nil {
			c.layers.infos[id] = in
		}
	}
	all := make(map[string]layerInfo, len(imgs))
	for _, img := range imgs {
		in, ok := c.layers.infos[img.ID]
		if !ok {
			in = layerInfo{err: errLayerUnread}
		}
		// Tailles du résumé courant (la part partagée change avec les autres
		// images), couches et historique du cache.
		in.size, in.shared = img.Size, img.SharedSize
		all[img.ID] = in
	}
	return buildLayerIndex(all)
}

var errLayerUnread = errors.New("couches non lues")

// unusedImageIDs : images qu'aucun conteneur n'utilise et qui ne servent de
// base à aucune autre, celles que le nettoyage peut proposer.
func unusedImageIDs(imgs []image.Summary) []string {
	parents := map[string]bool{}
	for _, img := range imgs {
		if img.ParentID != "" {
			parents[img.ParentID] = true
		}
	}
	var out []string
	for _, img := range imgs {
		if img.Containers == 0 && !parents[img.ID] {
			out = append(out, img.ID)
		}
	}
	return out
}

// fetchLayerInfos lit couches et historique de chaque image, en parallèle.
func fetchLayerInfos(ctx context.Context, cli mobyclient.APIClient, imgs []image.Summary) map[string]layerInfo {
	out := make(map[string]layerInfo, len(imgs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, img := range imgs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			in := layerInfo{size: img.Size, shared: img.SharedSize}
			ins, err := cli.ImageInspect(ctx, img.ID)
			if err == nil {
				in.diffIDs = ins.RootFS.Layers
				var h mobyclient.ImageHistoryResult
				h, err = cli.ImageHistory(ctx, img.ID)
				in.hist = h.Items
			}
			in.err = err
			mu.Lock()
			out[img.ID] = in
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}
