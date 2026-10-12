package compose

// Exécution du nettoyage : supprime exactement les éléments choisis, par
// identifiant et sans forcer. Si un élément s'est remis à servir depuis
// l'analyse (conteneur créé sur une image, volume monté), le démon refuse
// et l'élément est signalé en erreur, sans rien toucher d'autre.

import (
	"context"
	"fmt"
	"sync"

	mobyclient "github.com/moby/moby/client"
)

// Label : libellé d'une catégorie, tel qu'affiché.
func (k CleanupKind) Label() string {
	switch k {
	case CleanBuildCache:
		return "Cache de build"
	case CleanDanglingImage:
		return "Images pendantes <none>"
	case CleanUnusedNetwork:
		return "Réseaux inutilisés"
	case CleanUnusedImage:
		return "Images inutilisées"
	case CleanStackImage:
		return "Images de stacks arrêtées"
	case CleanOrphanVolume:
		return "Volumes orphelins"
	case CleanAnonymousVolume:
		return "Volumes anonymes"
	}
	return "?"
}

// cleanupParallel : suppressions simultanées dans une catégorie.
const cleanupParallel = 4

// CleanupLive supprime items et rend compte de chaque élément sur le flux de
// progression habituel : une « stack » par catégorie, une ressource par
// élément. Le flux se termine par la sentinelle AllDone.
func (c *Client) CleanupLive(ctx context.Context, items []CleanupItem) <-chan ProgressEvent {
	ch := make(chan ProgressEvent, 200)
	go func() {
		defer func() { ch <- ProgressEvent{AllDone: true} }()
		byKind := map[CleanupKind][]CleanupItem{}
		var kinds []CleanupKind
		for _, it := range items {
			if _, ok := byKind[it.Kind]; !ok {
				kinds = append(kinds, it.Kind)
			}
			byKind[it.Kind] = append(byKind[it.Kind], it)
		}
		// Comme runLive : les catégories à venir s'annoncent d'emblée, et
		// celles qu'une annulation devance ne sont jamais commencées.
		for _, k := range kinds {
			ch <- ProgressEvent{StackName: k.Label(), StackQueued: true}
		}
		for _, k := range kinds {
			label := k.Label()
			if ctx.Err() != nil {
				ch <- ProgressEvent{StackName: label, StackDone: true, Err: ctx.Err()}
				continue
			}
			ch <- ProgressEvent{StackName: label, StackStarted: true}
			var (
				mu       sync.Mutex
				firstErr error
				wg       sync.WaitGroup
				sem      = make(chan struct{}, cleanupParallel)
			)
			for _, it := range byKind[k] {
				// L'attente d'une place cède aussi à l'annulation : sinon
				// l'élément suivant partait avec un contexte annulé.
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
				}
				if ctx.Err() != nil {
					break // une place prise en trop reste dans sem, local à la catégorie
				}
				wg.Add(1)
				go func() {
					defer func() { <-sem; wg.Done() }()
					id := cleanupResourceID(it)
					send(ctx, ch, ProgressEvent{StackName: label, Container: id, Status: "Suppression", State: "working"})
					detail, err := c.removeCleanupItem(ctx, it)
					ev := ProgressEvent{StackName: label, Container: id, Status: "Supprimé", Details: detail, State: "done"}
					if err != nil {
						ev.Status, ev.Details, ev.State = err.Error(), "", "error"
						mu.Lock()
						if firstErr == nil {
							firstErr = err
						}
						mu.Unlock()
					}
					ch <- ev
				}()
			}
			wg.Wait()
			if firstErr == nil && ctx.Err() != nil {
				firstErr = ctx.Err()
			}
			ch <- ProgressEvent{StackName: label, StackDone: true, Err: firstErr}
		}
	}()
	return ch
}

// send : envoi d'un événement intermédiaire, abandonné si personne ne lit
// et que l'opération est annulée.
func send(ctx context.Context, ch chan<- ProgressEvent, ev ProgressEvent) {
	select {
	case ch <- ev:
	case <-ctx.Done():
	}
}

// cleanupResourceID : nom unique d'un élément dans la progression (deux
// images pendantes portent souvent le même nom).
func cleanupResourceID(it CleanupItem) string {
	switch it.Kind {
	case CleanDanglingImage, CleanUnusedImage, CleanStackImage:
		short := it.ID
		if len(short) > 19 {
			short = short[7:19] // après « sha256: »
		}
		return it.Name + " " + short
	}
	return it.Name
}

// removeCleanupItem supprime un élément ; detail décrit ce qui a été
// récupéré quand le démon le dit.
func (c *Client) removeCleanupItem(ctx context.Context, it CleanupItem) (detail string, err error) {
	api := c.cli.Client()
	switch it.Kind {
	case CleanBuildCache:
		// Tout le cache inutilisé, comme « docker builder prune -a » : c'est ce
		// que le démon compte comme libérable.
		res, err := api.BuildCachePrune(ctx, mobyclient.BuildCachePruneOptions{All: true})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d entrées, %s récupérés", len(res.Report.CachesDeleted), humanSize(int64(res.Report.SpaceReclaimed))), nil
	case CleanDanglingImage, CleanUnusedImage, CleanStackImage:
		// Une image étiquetée plusieurs fois ne se supprime par identifiant
		// qu'en forçant : on retire chaque étiquette, la dernière supprime
		// l'image. Jamais Force : une image remise en service est refusée.
		refs := it.Refs
		if len(refs) == 0 {
			refs = []string{it.ID}
		}
		// Retirer une étiquette d'une image en service réussit tant qu'il en
		// reste une : seule la dernière serait refusée, et l'image resterait
		// amputée de ses autres étiquettes. On vérifie donc d'abord qu'aucun
		// conteneur ne l'utilise (créé depuis l'analyse).
		f := make(mobyclient.Filters).Add("ancestor", it.ID)
		used, err := api.ContainerList(ctx, mobyclient.ContainerListOptions{All: true, Filters: f})
		if err != nil {
			return "", err
		}
		if n := len(used.Items); n > 0 {
			return "", fmt.Errorf("utilisée depuis l'analyse par %d conteneur(s), conservée", n)
		}
		for _, r := range refs {
			if _, err := api.ImageRemove(ctx, r, mobyclient.ImageRemoveOptions{PruneChildren: true}); err != nil {
				return "", err
			}
		}
		return "", nil
	case CleanUnusedNetwork:
		_, err := api.NetworkRemove(ctx, it.ID, mobyclient.NetworkRemoveOptions{})
		return "", err
	case CleanOrphanVolume, CleanAnonymousVolume:
		_, err := api.VolumeRemove(ctx, it.ID, mobyclient.VolumeRemoveOptions{})
		return "", err
	}
	return "", fmt.Errorf("catégorie inconnue %d", it.Kind)
}

// humanSize : taille lisible, mêmes unités que l'interface (Go = 2³⁰).
func humanSize(b int64) string {
	if b >= 1<<30 {
		return fmt.Sprintf("%.1f Go", float64(b)/(1<<30))
	}
	return fmt.Sprintf("%d Mo", b>>20)
}
