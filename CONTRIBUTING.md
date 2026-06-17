# Contribuer à DockStack

Merci de contribuer ! Quelques conventions pour garder le projet cohérent.

## Prérequis

- Go (version définie dans [`go.mod`](go.mod))
- Docker / Docker Compose (DockStack pilote des stacks Compose)

DockStack est **Linux-only** (métriques via `/proc`, `statfs`).

## Lancer le projet

```bash
go run ./cmd/dockstack
```

## Avant de pousser

La CI vérifie le formatage, l'analyse statique et les tests. Lance-les en local :

```bash
gofmt -l .        # doit ne rien afficher (sinon : gofmt -w .)
go vet ./...
go build ./...
go test -race ./...
```

## Style de code

- Commentaires et docstrings **en français**.

## Messages de commit

Les messages suivent le format **[Conventional Commits](https://www.conventionalcommits.org/)** :

```
<type>: <description>
```

Types utilisés :

| Type     | Usage                                  | Apparaît dans les notes de version |
|----------|----------------------------------------|------------------------------------|
| `feat:`  | Nouvelle fonctionnalité                | ✅ section « Nouveautés »           |
| `fix:`   | Correction de bug                      | ✅ section « Corrections »          |
| `docs:`  | Documentation seulement                | ❌                                  |
| `test:`  | Tests seulement                        | ❌                                  |
| `ci:`    | CI / outillage de build                | ❌                                  |
| `chore:` | Tâches diverses (deps, ménage…)        | ❌                                  |

> **Pourquoi c'est important :** les notes de version sont générées
> automatiquement à partir des messages de commit par GoReleaser
> (voir [`.goreleaser.yaml`](.goreleaser.yaml)). Un commit `feat:` ou `fix:`
> rédigé clairement se retrouve tel quel dans la release.

## Publier une version

Les releases sont déclenchées par un tag `v*` ; GoReleaser compile les binaires
et publie la GitHub Release avec les notes générées :

```bash
git tag v0.3.0
git push origin v0.3.0
```
