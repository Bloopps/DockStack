package compose

// Commandes prune de la CLI, avec leur sémantique exacte : mêmes appels au
// démon, mêmes filtres. « docker volume prune -a » est volontairement absent
// (il effacerait les données des stacks arrêtées, cf. docs/PLAN-NETTOYAGE.md).

import (
	"context"
	"fmt"

	mobyclient "github.com/moby/moby/client"
)

// PruneCommand : une commande prune de la CLI.
type PruneCommand int

const (
	PruneSystem     PruneCommand = iota // docker system prune
	PruneImages                         // docker image prune (pendantes)
	PruneImagesAll                      // docker image prune -a
	PruneBuilder                        // docker builder prune (cache pendant)
	PruneBuilderAll                     // docker builder prune -a
	PruneNetworks                       // docker network prune
	PruneContainers                     // docker container prune
	PruneVolumes                        // docker volume prune (anonymes seulement)
)

// PruneCommands : toutes les commandes proposées, dans l'ordre affiché.
var PruneCommands = []PruneCommand{PruneSystem, PruneImages, PruneImagesAll, PruneBuilder,
	PruneBuilderAll, PruneNetworks, PruneContainers, PruneVolumes}

// CLI : la commande telle qu'on la taperait.
func (p PruneCommand) CLI() string {
	return [...]string{"docker system prune", "docker image prune", "docker image prune -a",
		"docker builder prune", "docker builder prune -a", "docker network prune",
		"docker container prune", "docker volume prune"}[p]
}

// Steps : étapes élémentaires ; « system prune » enchaîne celles de la CLI
// (conteneurs arrêtés, réseaux inutilisés, images pendantes, cache pendant).
func (p PruneCommand) Steps() []PruneCommand {
	if p == PruneSystem {
		return []PruneCommand{PruneContainers, PruneNetworks, PruneImages, PruneBuilder}
	}
	return []PruneCommand{p}
}

// stepLabel : libellé d'une étape dans la progression.
func (p PruneCommand) stepLabel() string {
	return [...]string{"", "Images pendantes", "Images sans conteneur", "Cache de build pendant",
		"Cache de build inutilisé", "Réseaux inutilisés", "Conteneurs arrêtés", "Volumes anonymes"}[p]
}

// PruneLive exécute la commande et rend compte de chaque étape sur le flux
// de progression habituel. Fin par la sentinelle AllDone.
func (c *Client) PruneLive(ctx context.Context, p PruneCommand) <-chan ProgressEvent {
	ch := make(chan ProgressEvent, 32)
	go func() {
		defer func() { ch <- ProgressEvent{AllDone: true} }()
		var firstErr error
		for _, step := range p.Steps() {
			if ctx.Err() != nil {
				firstErr = ctx.Err()
				break
			}
			label := step.stepLabel()
			send(ctx, ch, ProgressEvent{StackName: p.CLI(), Container: label, Status: "Nettoyage", State: "working"})
			n, freed, err := c.pruneStep(ctx, step)
			detail := humanSize(freed) + " récupérés"
			if n >= 0 {
				detail = fmt.Sprintf("%d supprimé%s · %s", n, plural(n), detail)
			}
			ev := ProgressEvent{StackName: p.CLI(), Container: label, Status: "Fait", Details: detail, State: "done"}
			if err != nil {
				ev.Status, ev.Details, ev.State = err.Error(), "", "error"
				if firstErr == nil {
					firstErr = err
				}
			}
			ch <- ev
		}
		ch <- ProgressEvent{StackName: p.CLI(), StackDone: true, Err: firstErr}
	}()
	return ch
}

func plural(n int) string {
	if n > 1 {
		return "s"
	}
	return ""
}

// pruneStep appelle le démon ; n : objets supprimés, freed : octets
// récupérés selon le démon.
func (c *Client) pruneStep(ctx context.Context, p PruneCommand) (n int, freed int64, err error) {
	api := c.cli.Client()
	switch p {
	case PruneImages, PruneImagesAll:
		// Comme la CLI : « dangling=true » sans -a, « dangling=false » avec.
		f := make(mobyclient.Filters).Add("dangling", fmt.Sprint(p == PruneImages))
		res, err := api.ImagePrune(ctx, mobyclient.ImagePruneOptions{Filters: f})
		if err != nil {
			return 0, 0, err
		}
		// Le compte rendu mêle images et couches supprimées : pas de
		// décompte, seulement l'espace récupéré.
		return -1, int64(res.Report.SpaceReclaimed), nil
	case PruneBuilder, PruneBuilderAll:
		res, err := api.BuildCachePrune(ctx, mobyclient.BuildCachePruneOptions{All: p == PruneBuilderAll})
		if err != nil {
			return 0, 0, err
		}
		return len(res.Report.CachesDeleted), int64(res.Report.SpaceReclaimed), nil
	case PruneNetworks:
		res, err := api.NetworkPrune(ctx, mobyclient.NetworkPruneOptions{})
		if err != nil {
			return 0, 0, err
		}
		return len(res.Report.NetworksDeleted), 0, nil
	case PruneContainers:
		res, err := api.ContainerPrune(ctx, mobyclient.ContainerPruneOptions{})
		if err != nil {
			return 0, 0, err
		}
		return len(res.Report.ContainersDeleted), int64(res.Report.SpaceReclaimed), nil
	case PruneVolumes:
		res, err := api.VolumePrune(ctx, mobyclient.VolumePruneOptions{})
		if err != nil {
			return 0, 0, err
		}
		return len(res.Report.VolumesDeleted), int64(res.Report.SpaceReclaimed), nil
	}
	return 0, 0, fmt.Errorf("commande inconnue %d", p)
}
