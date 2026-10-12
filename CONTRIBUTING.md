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

Tests d'intégration contre un vrai démon Docker (hors CI) : ils créent puis
suppriment des projets jetables `zzdsprobe*` avec l'image `busybox`, sans
toucher aux stacks existantes.

```bash
go test -tags integration -count=1 ./internal/compose/ ./internal/tui/
```

`./internal/tui/` contient `TestLiveJourney` : le parcours de l'interface
sur les vraies stacks du dossier configuré, en lecture seule (actions et logs
simulés, `HOME` isolé).

### Tests longs et mesures

| Variable / commande | Effet |
|---|---|
| `DOCKSTACK_MONKEY=200 go test -run 'TestMonkey' ./internal/tui/` | frappes aléatoires sur 200 graines au lieu de 12 (≈ 6 min) |
| `go test -short ./...` | saute les tests du faux démon qui tombe (≈ 30 s) |
| `DOCKSTACK_MEASURE=1 go test -tags integration -run TestMeasure -v ./internal/tui/` | mesures longues : logs en suivi, contre-pression d'un pull (projets `zzdsprobe*`, images jetables supprimées après) |
| `DOCKSTACK_MEASURE_PROF=<dossier>` | ajoute un profil CPU des mesures de logs |

### Fuzzing

Les graines de chaque cible tournent avec `go test` ordinaire, donc en CI.
Pour fuzzer réellement (une cible à la fois) :

```bash
go test -run XXX -fuzz '^FuzzScanComposeDir$' -fuzztime 3m ./internal/compose/
```

| Paquet | Cibles |
|---|---|
| `internal/compose` | `FuzzScanComposeDir`, `FuzzLoadProjectDotEnv`, `FuzzCgroupParsers` |
| `internal/tui` | `FuzzSortByState`, `FuzzDiffCapture`, `FuzzCaptureFile` |
| `internal/theme` | `FuzzParse`, `FuzzSchemeReadable` |

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
