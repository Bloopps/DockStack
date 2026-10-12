# Prompt : passe complète performance + débogage de dockstack

> À coller tel quel dans une nouvelle session Claude Code ouverte dans
> `/home/ubuntu/dockerstack`.

---

Tu fais une **passe complète d'optimisation des performances et de chasse aux
bugs** sur dockstack, un TUI Go (Go 1.26, bubbletea v2 / bubbles v2 / lipgloss
v2, `github.com/docker/compose/v5` en API programmatique) qui pilote des stacks
docker compose. L'objectif est de trouver **et corriger** ce qui est lent,
gourmand ou faux, avec des preuves chiffrées et des tests, sans casser le
comportement actuel.

Réponds et rédige en français.

## 1. Contexte à lire avant toute chose

- `README.md`, `CONTRIBUTING.md` (commandes CI, conventions).
- `docs/AUDIT-PERF-2026-09-30.md` : audit précédent. Les points 1 à 6 sont
  **déjà faits** (stats lues dans les cgroups, 20 fps, filtre d'événements côté
  démon, taille des images relue sur événement, table des montages mémorisée,
  mise en page maison dans `internal/tui/screen.go`). Ne les refais pas ;
  réutilise sa section « Méthode » pour mesurer, et compare-toi à ses chiffres
  (repos ≈ 1,7 % d'un cœur, 170 réveils/s, rendu 0,81 ms, démarrage < 80 ms,
  RSS 40 Mo, 27 goroutines).
- `git status` / `git log --oneline -15` : l'arbre de travail contient des
  modifications **non commitées** (points 2 à 6 de l'audit). Pars de cet état,
  ne fais ni `stash`, ni `reset`, ni `checkout` qui les perdrait.
- Fichiers centraux : `internal/tui/model.go`, `keys.go`, `stacklist.go`,
  `header.go`, `stats.go`, `progressview.go`, `commands.go`, `screen.go` ;
  `internal/compose/client.go`, `plan.go`, `files.go`, `cgroup.go` ;
  `internal/monitor/metrics.go` ; `internal/backup/backup.go` ;
  `internal/theme/`.

## 2. Règles non négociables

1. **Ne jamais écrire dans `~/.dockstack`** (config et captures réelles de
   l'utilisateur). Tout test, harnais ou lancement de mesure tourne avec un
   `HOME` temporaire (copie de la config si besoin). `internal/tui/main_test.go`
   isole déjà `HOME` pour les tests TUI : garde ce garde-fou partout où
   `cfg.Save` ou `backup.Save` peut être appelé.
2. **Aucune action compose réelle sur les stacks existantes** (up/down/stop/
   restart/recreate/pull/remove). La machine fait tourner une vraie infra
   (~60 stacks, cible de conception : ~600 conteneurs). Les tests d'intégration
   n'utilisent que des projets jetables `zzdsprobe*` (image `busybox`) et les
   suppriment. Pour exercer l'UI, utilise un harnais où `ListStacks`/`Usage`
   viennent du vrai client mais où les `*ManyLive` sont simulés.
3. Lectures seules autorisées sur le démon (`ps`, `inspect`, `events`, `df`,
   `docker compose --dry-run`).
4. Le dépôt doit rester **gofmt-clean**, `go vet` propre, et
   `go test -race ./...` vert. Ne reformate rien au-delà de gofmt.
5. Commentaires et docstrings **en français**. Ne traduis pas l'existant que tu
   ne touches pas.
6. **Parité CLI** : l'affichage et le comportement doivent rester identiques à
   `docker compose` (PlanUp == `docker compose --dry-run up`, chargement du
   `.env`, override files, `WithoutUnnecessaryResources`,
   `WithDiscardEnvFiles`). Une optimisation qui change un résultat est un bug.
7. Toute optimisation du rendu doit rester **identique octet pour octet**
   (modèle : `TestScreenMatchesLipgloss`, 15 vues × 8 tailles). Ne jamais
   imbriquer de spans stylés dans un wrapper `Reverse(true)`.
8. Invariants du TUI à respecter : toute mutation de stacks/filtre/groupes
   repliés appelle `invalidateRows()` ; sélection par **nom** de stack, jamais
   par index ; `cursorStack()`/`visibleStacks()`/`cursorRow()` sont les helpers
   canoniques ; messages asynchrones protégés par numéro de séquence
   (`progressSeq`, `logSeq`, `refreshGen`…).
9. Piège Go connu : n'écris pas `return m, f(m.startX())` (ordre d'évaluation
   de `m` non spécifié) ; calcule la commande avant le `return`.
10. **Ne commite pas et ne pousse pas** sans demande explicite. Si on te le
    demande : commits atomiques au format Conventional Commits (`perf:`,
    `fix:`, `test:`, `docs:`…), identité `dev <dev@localhost>`, historique
    linéaire sur `master`, jamais de commit de merge.
11. Commandes de plus de 30 s (benchmarks, profils, tests d'intégration) :
    lance-les en arrière-plan avec un fichier de log.

## 3. Déroulé attendu

### Phase 0 : ligne de base

- `gofmt -l .`, `go vet ./...`, `go build ./...`, `go test -race ./...`,
  `go test -tags integration -count=1 ./internal/compose/`. Note tout échec
  **avant** de modifier quoi que ce soit.
- Mesure au repos (3 séries, méthode de l'audit) : CPU app, CPU dockerd
  attribuable, réveils/s, RSS, goroutines. Démarrage (premier écran, liste
  chargée). Benchmarks de rendu existants.
- Consigne les chiffres dans un tableau : ce sera la référence avant/après.

### Phase 1 : performance (ce que l'audit précédent n'a pas couvert)

Profile (`pprof` CPU, heap, allocs, goroutine, `trace`) et chiffre chaque piste
avant d'y toucher :

- **Montée en charge** : simule 600 stacks / 600 conteneurs (faux client ou
  répertoires compose générés sous un dossier temporaire, jamais déployés).
  Coût de `ListStacks`, du scan `scanComposeDir`, du tri, du filtre, de
  `listRows()`, du header et des stats. Repère tout ce qui est O(n²) ou relu à
  chaque rafraîchissement (15 s) alors que rien n'a changé.
- **Chargement des projets compose** : coût de `loadProject`, `PlanUp`,
  `DeclaredServices` par stack ; caches existants (mtime) : sont-ils efficaces,
  corrects, invalidés au bon moment ?
- **Vue de progression sous forte charge** : un pull de plusieurs stacks génère
  des milliers d'événements de couches. Mesure le débit, la contre-pression
  (drop des `Working`, envois terminaux bloquants), les allocations par
  événement, le coût de rendu quand le bloc dépasse l'écran.
- **Logs en suivi** : débit maximal soutenu (stack bavarde), coût du batch
  `logBatchMax`, du ring buffer 10k, du rendu et du défilement ; mémoire après
  plusieurs ouvertures/fermetures.
- **Allocations par frame** dans `View()` sur chaque vue (liste, services,
  détail, compose plein écran, captures, thèmes, aide) : chaînes reconstruites
  à chaque frame qui pourraient être mémoïsées, `StringWidth` répétés,
  `fmt.Sprintf` dans les boucles chaudes.
- **Sélecteur de thèmes** : aperçu en direct sur 658 thèmes, `applyScheme`,
  recherche `/` ; coût d'un changement de thème.
- **Métriques hôte** (`internal/monitor`) : fréquence de lecture de `/proc`,
  allocations, lectures redondantes.
- **Démarrage** et **taille du binaire** (43 Mo) : ce qui pèse, ce qui peut être
  différé.

Pour chaque optimisation retenue : benchmark ou mesure avant/après, test qui
verrouille le comportement, et gain chiffré. Écarte explicitement les pistes
dont le gain est dans le bruit.

### Phase 2 : débogage et robustesse

Cherche activement les bugs, en les **reproduisant** avant de corriger :

- **Concurrence** : `go test -race` sur tout, plus des tests ciblés sur les
  pompes de canaux (progression, logs, événements Docker, usage). Fuites de
  goroutines : compte les goroutines avant/après ouverture-fermeture des logs,
  lancement-annulation d'une op, changement de répertoire, `R` reload,
  reconnexion. Contextes jamais annulés, timers/tickers jamais arrêtés.
- **Séquençage des messages** : messages tardifs d'une op, d'un reload ou d'un
  flux de logs précédent qui arrivent après un changement de vue ; double
  pompe sur un canal ; `opDoneMsg` qui modifie la mauvaise vue.
- **Démon Docker** : démon arrêté au lancement, redémarré pendant une op,
  pendant un suivi de logs, pendant l'abonnement aux événements (boucle chaude
  si le canal se ferme ?), API ancienne (< 1.48), démon lent (timeouts).
- **Système de fichiers** : dossier de stacks illisible, compose invalide,
  `.env` absent ou malformé, liens symboliques en boucle, stack supprimée
  pendant l'affichage, `~/.dockstack` en lecture seule ou appartenant à root,
  capture JSON corrompue ou tronquée.
- **cgroups** : conteneur qui disparaît entre deux lectures, cgroup v1, driver
  cgroupfs, démon distant (l'ID doit figurer dans la feuille), valeurs qui
  reculent (CPU négatif ?).
- **Rendu** : terminaux très étroits ou très bas (1×1, 20×5, 300×100),
  redimensionnement pendant chaque vue, noms de stacks/services très longs,
  Unicode large (CJK, emoji), zéro stack, zéro conteneur, liste filtrée vide,
  curseur hors bornes après un refresh qui retire des stacks.
- **Clavier** : chaque touche dans chaque vue, y compris pendant une op en
  cours et pendant la saisie d'un filtre (`typing()`), verr. maj.
- **Fuzzing** (`go test -fuzz`, quelques minutes chacun) sur les parseurs :
  thèmes Ghostty, captures JSON et format texte hérité, `.env`, détection de
  fichiers compose, calcul d'état agrégé.

Pour chaque bug : scénario de reproduction, cause, correction minimale, test de
non-régression qui échoue avant et passe après.

### Phase 3 : validation

- `gofmt -l .` vide, `go vet ./...`, `go build ./...`, `go test -race ./...`,
  tests d'intégration réels, `govulncheck ./...`.
- Parité : `PlanUp` comparé à `docker compose --dry-run up` sur toutes les
  stacks en marche (lecture seule ; attention, la sortie dry-run finit par une
  espace).
- Parcours UI rejoués dans le harnais (HOME isolé, actions simulées) : liste,
  ↩ panneau d'actions et chaque entrée, `v` compose plein écran, `→` mode
  services, `u` sur une stack non déployée, `l` logs, `b` captures et
  relance, `t` thèmes, filtre `/`, `a` afficher tout.
- Mesures de la phase 0 refaites dans les mêmes conditions (3 séries).

## 4. Livrables

1. Un rapport `docs/AUDIT-PERF-DEBUG-<date>.md`, en français, structuré ainsi :
   résumé ; tableau des mesures avant/après ; bugs trouvés classés
   🔴 critique / 🟡 moyen / ⚪ mineur avec reproduction, cause, correctif et
   test ; optimisations appliquées avec gain chiffré ; pistes écartées et
   pourquoi ; ce qui n'a **pas** pu être vérifié ; méthode pour refaire les
   mesures.
2. Les corrections dans l'arbre de travail, chacune couverte par un test.
3. Un récapitulatif final court : ce qui a été trouvé, corrigé, mesuré, et ce
   qui reste ouvert avec une recommandation.

Sois honnête sur la vérification : ne dis « validé » que pour ce qui a été
réellement exécuté, et dis explicitement ce qui ne l'a pas été.
