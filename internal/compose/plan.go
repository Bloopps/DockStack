package compose

import (
	"context"
	"sort"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/docker/compose/v5/pkg/compose"
	"github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"
)

// Recreation décrit un service en marche qu'un « up » recréerait (donc
// interromprait brièvement) et pourquoi.
type Recreation struct {
	Service string
	Reason  string // « config modifiée » ou « image mise à jour »
}

// PlanUp prévoit quels services EN MARCHE un up recréerait, en reproduisant le
// critère de compose (RecreateDiverged) : hash de configuration du service
// différent de celui du conteneur, ou image locale plus récente que celle du
// conteneur (après un pull). C'est une prévision : elle sert à avertir avant
// une confirmation, sans jamais bloquer l'action.
func (c *Client) PlanUp(ctx context.Context, stack Stack) ([]Recreation, error) {
	proj, _, err := loadScopedProjectReport(&stack)
	if err != nil {
		return nil, err
	}
	f := make(mobyclient.Filters).Add("label", api.ProjectLabel+"="+proj.Name)
	res, err := c.cli.Client().ContainerList(ctx, mobyclient.ContainerListOptions{Filters: f})
	if err != nil {
		return nil, err
	}
	reasons := map[string]string{}
	imageID := map[string]string{} // cache ref d'image → id local
	for _, ctr := range res.Items {
		if ctr.State != container.StateRunning {
			continue
		}
		name := ctr.Labels[api.ServiceLabel]
		sv, ok := proj.Services[name]
		if !ok || reasons[name] != "" {
			continue
		}
		if want, err := compose.ServiceHash(sv); err == nil {
			if have := ctr.Labels[api.ConfigHashLabel]; have != "" && have != want {
				reasons[name] = "config modifiée"
				continue
			}
		}
		if have := ctr.Labels[api.ImageDigestLabel]; have != "" && sv.Image != "" {
			id, cached := imageID[sv.Image]
			if !cached {
				if ins, err := c.cli.Client().ImageInspect(ctx, sv.Image); err == nil {
					id = ins.ID
				}
				imageID[sv.Image] = id
			}
			if id != "" && id != have {
				reasons[name] = "image mise à jour"
			}
		}
	}
	out := make([]Recreation, 0, len(reasons))
	for n, r := range reasons {
		out = append(out, Recreation{Service: n, Reason: r})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out, nil
}
