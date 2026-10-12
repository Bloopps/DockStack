# Passe performance et débogage (1er octobre 2026)

Passe complète sur dockstack, partie de l'état non commité qui contient les
points 2 à 6 de `AUDIT-PERF-2026-09-30.md`. Toutes les valeurs sont
**mesurées** sur la machine de production (8 cœurs, cgroups v2, Docker 29.2,
63 stacks dont 15 en marche, 72 conteneurs), avec un `HOME` isolé et sans
aucune action compose sur les stacks existantes.

## Résumé

- **13 bugs corrigés**, chacun reproduit par un test qui échouait avant la
  correction : 1 critique, 4 moyens, 8 mineurs.
- Le bug critique : la vue de progression **paniquait** dès que le terminal
  était moins haut que l'en-tête. L'application quittait alors en pleine
  opération compose.
- Le plus gênant au quotidien : face à un démon Docker qui ne répond plus,
  l'écran restait **indéfiniment** sur « Chargement… ».
- **4 optimisations** chiffrées. Les deux plus utiles portent sur les
  situations chargées : un gros pull coûte 44 fois moins de CPU, et un
  rafraîchissement à 600 stacks avec un orphelin passe de ≈ 500 ms à 10 ms.
- Au repos, l'application était déjà sobre après l'audit précédent. Les
  chiffres restent dans le même ordre (voir le tableau).
- Ligne de base saine : gofmt, vet, `go test -race` et les 46 tests
  d'intégration réels étaient verts avant toute modification.

## Mesures avant / après

Méthode de l'audit précédent (pseudo-terminal 190 × 50, fenêtres de 45 s,
3 séries, `HOME` isolé, binaires construits depuis l'arbre de travail).

| Au repos (3 séries) | Avant | Après |
|---|---|---|
| CPU de dockstack | 1,67 / 1,71 / 1,64 % | 1,58 / 1,58 / 1,58 % |
| CPU de dockerd pendant la série | 2,38 / 2,29 / 2,29 % | 2,42 / 2,16 / 2,22 % |
| Réveils de dockstack (changements de contexte) | ≈ 165 /s | ≈ 175 /s |
| Mémoire résidente | 40,0 à 40,6 Mo | 39,4 Mo |
| Threads / goroutines | 14 / 24 à 26 | 14 à 15 / non remesuré |
| Premier écran | 73 à 75 ms | 73 à 76 ms |
| Liste chargée | 76 à 80 ms | 76 à 80 ms (une mesure à 128 ms) |

Au repos, les écarts sont dans le bruit : CPU −0,1 pt, réveils +6 %. Le
démon reste à son niveau sans instance (2,2 à 2,6 %). La prise de contact
bornée avec le démon ajoute un aller-retour au démarrage, sans effet mesurable
hors d'une mesure isolée.

| Rendu et débit (benchmarks) | Avant | Après |
|---|---|---|
| Écran complet de la liste, 63 stacks | 0,82 ms | 0,82 ms |
| Rafraîchissement à 600 stacks (liste + rendu) | 1,70 ms | 1,76 ms |
| Frappe ↓ à 600 stacks | 0,83 ms | 0,84 ms |
| Progression, 3 000 événements en rafale | 777 µs/événement | 18 µs/événement |
| Lot de 256 lignes de logs | 241 µs | 148 µs |
| Lecture cgroup d'un conteneur | 71 µs, 12,9 Ko | 53 µs, 0,86 Ko |
| Rafraîchissement, 600 stacks et un orphelin | 554 ms | ≈ 10 ms |

« Avant » désigne l'arbre de travail au début de la passe, points 2 à 6 de
l'audit précédent compris.

## Bugs

### 🔴 Critique

**1. La vue de progression panique sur un terminal bas.**
- Reproduction : une opération en cours, terminal de moins de 13 lignes
  (en-tête plus haut que l'écran), par exemple une fenêtre réduite ou un
  panneau tmux étroit. `TestViewsSurviveAnySize` : panique
  `slice bounds out of range [:-3]` en 40 × 10.
- Cause : `fitHeight` recevait une hauteur négative.
- Effet : bubbletea rattrape la panique et quitte. L'opération compose en
  cours est interrompue avec le processus.
- Correction : hauteur bornée à 0 (`model.go`, `fitHeight`).
- Test : `TestViewsSurviveAnySize`, toutes les vues de 1 × 1 à 300 × 100.

### 🟡 Moyens

**2. Démon muet : « Chargement… » pour toujours.**
- Reproduction : `DOCKER_HOST` vers un socket qui accepte les connexions
  sans répondre (démon figé, redémarrage bloqué). Au bout de 45 s, l'écran
  est toujours sur « Chargement… », et le délai de 30 s du chargement ne se
  déclenche jamais.
- Cause : le client moby négocie la version de l'API au premier appel, sous
  un `sync.Mutex` qui ignore le contexte. L'abonnement aux événements,
  lancé sans délai, prenait ce verrou et le gardait. Tous les autres appels
  attendaient derrière lui, délais compris.
- Correction : `compose.New` négocie tout de suite, avec un délai de 10 s.
  Passé ce délai : « le démon Docker ne répond pas (10s sans réponse) », et
  R réessaie.
- Tests : `TestNewHungDaemon`, `TestNewNoDaemon`. Vérifié dans le binaire réel
  (pseudo-terminal) : le message s'affiche au bout de 10 s.

**3. Liste d'un autre dossier affichée après un changement de dossier.**
- Reproduction : un chargement de la liste en vol (rafraîchissement
  automatique, R, démon lent), puis `o` vers un autre dossier. Le résultat
  périmé arrive après le nouveau et le remplace. Les stacks affichées et les
  actions visent alors l'ancien dossier. Même effet, plus bénin, quand deux
  chargements se croisent.
- La sélection, indexée par nom relatif, survivait aussi au changement de
  dossier : une stack de même nom dans le nouveau dossier héritait de la coche.
- Correction : le message de chargement porte le dossier et l'heure de
  départ. Un résultat d'un autre dossier, ou plus ancien que celui affiché,
  est écarté. Changer de dossier vide la liste, la sélection et le mode
  services.
- Tests : `TestStaleStacksLoadIgnored`, `TestDirChangeClearsSelection`.

**4. Parité CLI : `.env` mal formé ignoré.**
- Reproduction : un `.env` avec une ligne invalide. `docker compose config`
  échoue avec « failed to read …/.env: line 3: key cannot contain a space ».
  dockstack l'ignorait et interpolait ses variables à vide ou à leur valeur
  par défaut. Un up pouvait déployer une autre config que la CLI.
- Correction : `loadProject` renvoie l'erreur de lecture, avec le même
  message que la CLI au caractère près.
- Test : `TestLoadProjectMalformedDotEnv`. Les 63 vraies stacks chargent
  comme avant (2 erreurs préexistantes, que la CLI renvoie aussi).

**5. Le sélecteur de dossiers ne défile pas.**
- Reproduction : `o` sur un dossier de plus de lignes que l'écran (57
  sous-dossiers dans `/home/ubuntu`). Le curseur et le pied de page sortent
  de l'écran : 207 lignes pour un écran de 24.
- Correction : fenêtre qui suit le curseur, noms tronqués à la largeur.
- Test : `TestViewsSurviveAnySize` (« dossiers nombreux »). Vérifié à l'œil
  dans le binaire réel.

### ⚪ Mineurs

| # | Bug | Correction | Test |
|---|---|---|---|
| 6 | Une erreur de chargement laissait l'indicateur « actualisation… » affiché | remis à zéro avec l'erreur | `TestStacksLoadErrorClearsRefreshing` |
| 7 | Message d'erreur sur une seule ligne, coupé par le terminal | replié à la largeur | `TestErrorWrapsToWidth` |
| 8 | En-tête « aucune déployée » pendant le chargement ou démon injoignable | « … » tant que la liste n'est pas lue | `TestHeaderUnknownWhileLoading` |
| 9 | Dossier de stacks illisible affiché comme vide | erreur « permission denied » affichée | `TestFindComposeFilesUnreadableRoot` |
| 10 | Encadré « Accès Docker refusé » pour un chemin contenant « docker » (`/srv/docker`) ; refus via TCP non reconnu | détection sur les messages du client Docker | `TestRenderErrorNoSudo` |
| 11 | Lignes plus larges que l'écran : titre des captures en 80 colonnes, confirmation (nom long, avertissement), noms de dossiers | tronquées ou repliées | `TestViewsSurviveAnySize` |
| 12 | `truncate` comptait les caractères, pas les colonnes : CJK et emoji sortaient deux fois trop larges | `ansi.Truncate` | `TestTruncateDisplayWidth` |
| 13 | Le filtre du mode services débordait du volet pendant la saisie | on voit la fin de la saisie | `TestServiceFilterFitsPane` |

Le test de conformité du rendu (`TestScreenMatchesLipgloss`) croyait couvrir
le sélecteur de thèmes, mais la touche `t` ne l'ouvrait pas dans le test. Il
le couvre maintenant, et le rendu y est aussi identique octet pour octet.

## Optimisations appliquées

**1. Orphelins : projets compose mémorisés.** Dès qu'un conteneur orphelin
existe (watchtower), chaque rafraîchissement de la liste rechargeait le projet
compose de **toutes** les stacks sans conteneur. Ici, 48 projets, soit
≈ 170 ms. À 600 stacks : 554 ms, à chaque événement Docker et chaque minute.
Le nom du projet et les noms de conteneurs possibles sont désormais mémorisés
par stack, tant que le compose, l'override et le `.env` gardent leur date et
leur taille.

| 600 stacks sans conteneur, 1 orphelin | Temps |
|---|---|
| Avant, à chaque rafraîchissement | 554 ms |
| Après, premier passage | 466 ms |
| Après, passages suivants | ≈ 10 ms |

Test : `TestCachedContainerNames` (fichier modifié, `.env` ou override
ajoutés, erreur mémorisée).

**2. Progression : événements par lots.** bubbletea recalcule la vue après
chaque message, et un pull émet des milliers d'événements de couches. Un
rendu par événement plafonnait le débit vers 1 300 événements/s, à 100 %
d'un cœur. Les événements disponibles sont maintenant lus par lots de 256 au
plus, comme les logs.

| 3 000 événements de couches d'un coup | Par événement | Total |
|---|---|---|
| Avant | 777 µs | 2,3 s |
| Après | 17,8 µs | 53 ms |

Test : `TestProgressBatching` (ordre, fin lue au milieu d'un lot, lot
périmé ignoré).

**3. Lecture des cgroups sans allocation.** C'était 37 % des allocations au
repos. Tampon de lecture réutilisé, analyse de `cpu.stat`, `memory.stat` et
`io.stat` sans `bufio.Scanner` ni `strings.Fields`.

| Lecture d'un conteneur | Temps | Mémoire | Allocations |
|---|---|---|---|
| Avant | 71 µs | 12,9 Ko | 32 |
| Après | 53 µs | 0,86 Ko | 16 |

Test : `FuzzCgroupParsers` compare le résultat à l'implémentation d'origine
(557 000 entrées aléatoires, aucune divergence).

**4. Logs : expression régulière évitée.** `sanitizeLogLine` passait chaque
ligne dans une expression régulière, même sans séquence d'échappement.

| Lot de 256 lignes | Temps | Allocations |
|---|---|---|
| Avant | 241 µs | 2 308 |
| Après | 148 µs | 259 |

Test : `TestSanitizeLogLine`.

## Pistes écartées

| Piste | Mesure | Pourquoi écartée |
|---|---|---|
| Liste à 600 stacks | rafraîchissement 1,7 ms, frappe 0,8 ms, filtre 1,6 ms | rien de quadratique, seules les lignes visibles sont dessinées |
| Rendu de la liste | 0,8 ms, 6 % du CPU au repos | le reste est `Style.Render` de lipgloss, span par span ; gain faible au repos |
| Ordonnanceur Go au repos | ≈ 50 % du CPU restant | réveils de la cadence de rendu (20 images/s) et du `sysmon` de Go ; baisser encore la cadence dégraderait la progression |
| Garder les fichiers cgroup ouverts | évite open/close | 4 descripteurs par conteneur, 2 400 à 600 conteneurs, au-delà de la limite par défaut (1 024) |
| Métriques de l'hôte (`/proc`) | 0,47 ms et 42 Ko toutes les 2 s | 0,02 % d'un cœur |
| Sélecteur de thèmes | 1,7 ms par rendu, 1,9 ms par pas | seulement quand il est ouvert |
| Démarrage | 75 ms jusqu'au premier écran ; init des paquets 15 ms dont 7 ms pour le schéma JSON de compose-go | dépendance tierce ; le reste vient surtout du pseudo-terminal de mesure, qui ne répond pas aux requêtes du terminal |
| Taille du binaire | 43 Mo en développement | GoReleaser retire déjà les symboles : 30 Mo publiés |
| Remove sans `Project` passé à compose | — | vérifié sur une stack réelle : les volumes d'une stack déjà arrêtée sont bien supprimés (`TestLiveRemoveAfterDown`) |
| Cache des services déclarés invalidé par la seule date du compose | — | décision de l'utilisateur, maintenue |

## Vérifications

- `gofmt -l .` vide, `go vet ./...` (avec et sans `-tags integration`),
  `go test -race ./...` : vert.
- Tests d'intégration réels (`-tags integration`, projets `zzdsprobe*`) :
  tous verts (17 tests réels, parcours d'interface compris), aucun conteneur jetable restant. Nouveaux : `TestLiveRemoveAfterDown`,
  `TestLiveNoGoroutineLeak` (logs et événements ouverts puis annulés 5 fois,
  goroutines revenues à leur niveau).
- Parcours d'interface sur les 63 vraies stacks, actions et logs simulés,
  HOME isolé : `TestLiveJourney` (`a`, ↩ et chaque entrée du panneau,
  Compose, `u` sur une stack non déployée, `v`, `→`, `l`, up sur une stack
  en marche avec la vraie prévision, `b`, capture, relance, `t`, `/`).
- Parité : `PlanUp` comparé à `docker compose --dry-run up -d` sur les 15
  stacks en marche : **0 écart**.
- `govulncheck` (dernière version) : aucune vulnérabilité atteignable.
- Frappes aléatoires (`TestMonkeyKeys`) : 200 séquences de 400 touches avec
  redimensionnements, dans toutes les vues, pendant les opérations et les
  saisies. Ni panique, ni écran débordant. 12 séquences par défaut,
  `DOCKSTACK_MONKEY=200` pour la longue passe.
- Fuzzing : thèmes Ghostty (670 000 fichiers), lisibilité des couleurs
  (1,5 million de palettes), fichiers de capture JSON et texte jusqu'au rendu
  et à la relance (125 000), analyseurs cgroup (557 000).
- Démon absent et démon muet : rejoués dans le binaire réel (pseudo-terminal
  et VHS). Pas de boucle chaude, R reste utilisable, sortie normale.

## Ce qui n'a pas pu être vérifié

- **Redémarrage réel du démon** pendant une opération, un suivi de logs ou
  l'abonnement aux événements : la machine fait tourner une vraie infra, le
  démon n'a pas été arrêté. Couvert seulement par les faux sockets et par le
  code de réabonnement (2 s).
- **cgroups v1, pilote cgroupfs, démon distant, rootless** : non disponibles
  ici. Seuls les tests unitaires sur des arborescences factices les couvrent.
- **API Docker < 1.48** (prévision d'un up sans les manifestes) : démon local
  en 1.53.
- **Fuites de goroutines côté TUI** sur un vrai terminal pendant des heures :
  vérifié seulement sur 65 s (24 à 26 goroutines, tas stable) et par le test
  d'intégration ci-dessus.
- Rendu dans un vrai terminal pour chaque correction d'affichage : vérifié à
  l'œil seulement pour le sélecteur de dossiers et les écrans d'erreur ; le
  reste par les tests de largeur et de hauteur.

## Méthode, pour refaire les mesures

Celle de `AUDIT-PERF-2026-09-30.md`, outillée ainsi :

- **Repos et démarrage** : script Python en pseudo-terminal (190 × 50,
  `HOME` isolé avec une copie de la config). Il relève le premier écran, la
  liste chargée (apparition de « maj » au pied de page), puis le CPU de
  l'application et de `dockerd`, les changements de contexte, la RSS et les
  threads sur 45 s.
- **Benchmarks** :
  `go test ./internal/tui/ -run XXX -bench .` (vues, 600 stacks, débit de la
  progression, logs) et `go test ./internal/compose/ -bench ReadCgroup`.
- **Profil au repos** : un `main` temporaire qui écrit les profils CPU et
  allocations et le nombre de goroutines au bout de 60 s.
- **Orphelins à 600 stacks** : 600 dossiers compose générés dans un dossier
  temporaire, jamais déployés, et un orphelin simulé passé à `attachOrphans`.
- **Démon muet** : socket Unix qui accepte sans répondre, désigné par
  `DOCKER_HOST`.
- **Parité** : pour chaque stack en marche, `PlanUp` contre les lignes
  `Container x  Recreate` de `docker compose --dry-run up -d`, en lecture
  seule. Attention, la sortie dry-run finit par une espace.

---

# Passe complémentaire (1er octobre 2026, suite)

Reprise des angles morts de la passe ci-dessus, selon
`PROMPT-PASSE-COMPLEMENTS.md`. Mêmes règles : `HOME` isolé, aucune action
compose sur les stacks existantes, seulement des projets jetables
`zzdsprobe*` et des images absentes de la machine, supprimées après usage.

## Résumé

- **6 bugs corrigés**, chacun avec un test qui échouait avant : 3 moyens,
  3 mineurs.
- **1 optimisation** : le suivi d'un conteneur bavard coûtait 40 % d'un cœur à
  ≈ 490 lignes/s. Il en coûte 11 %.
- Le démon qui tombe en cours de route est maintenant couvert par un faux
  démon Docker (`internal/fakedocker`), sans toucher au vrai.
- Mesures faites : logs, contre-pression d'un pull réel, trace d'exécution,
  démarrage avec un terminal qui répond, thèmes, 30 minutes au repos.
- Ligne de base saine : gofmt, vet, `go test -race` et les 17 tests
  d'intégration réels étaient verts avant toute modification.

## Bugs

### 🟡 Moyens

**1. Une prévision d'up périmée lançait l'up sans nouvelle frappe.**
- Reproduction : `u` sur une stack en marche, puis `?` et retour à la liste
  avant la réponse de la prévision. La réponse arrivait sur la liste, vue
  d'origine, et l'up partait. Deux `u` rapprochés : la première réponse
  gagnait, la seconde tombait par hasard sur le verrou d'opération.
- Cause : `handlePlanUp` ne comparait que la vue d'origine.
- Correction : numéro de séquence `planSeq`, qui avance à chaque demande et à
  chaque changement de vue au clavier. Une réponse dont le numéro n'est pas le
  dernier est écartée, et le statut « Analyse des changements… » s'efface en
  quittant la vue.
- Tests : `TestStalePlanUpAfterViewRoundTrip`, `TestOnlyLatestPlanUpApplies`.

**2. Démon arrêté ou tombé affiché comme un refus d'accès.**
- Reproduction : démon qui tombe en cours de route (faux démon), puis `R`.
  L'écran affichait « Accès Docker refusé » et conseillait
  `sudo usermod -aG docker $USER`, sans le message d'erreur. Même chose avec un
  démon arrêté sur `/var/run/docker.sock`.
- Cause : la seule présence de `docker.sock` dans le message suffisait. Un
  flux coupé (`error during connect: … docker.sock …: EOF`) et un démon arrêté
  la contiennent.
- Correction : « Accès Docker refusé » seulement pour un vrai refus
  (`permission denied` sur la connexion au démon). Les erreurs de connexion du
  client Docker et le démon muet affichent « Démon Docker injoignable »,
  l'action à faire et le message d'origine replié.
- Tests : `TestRenderErrorDaemonUnreachable` sur 5 messages réels du client,
  `TestDaemonDownAndBack`. Vérifié à l'œil dans le binaire réel (VHS, démon
  muet).

**3. Coller du texte dans un champ ne faisait rien.**
- Reproduction : coller dans le filtre `/` de la liste, des services ou des
  thèmes, ou dans le nom d'une capture. bubbletea v2 active le collage
  encadré (« bracketed paste ») et livre un `tea.PasteMsg`, que l'application
  ignorait.
- Correction : `handlePaste` insère le texte dans le champ en cours de saisie.
  Sauts de ligne et tabulations deviennent des espaces, les autres caractères
  de contrôle disparaissent, 64 caractères au plus. Hors saisie, un collage
  est ignoré : il ne devient jamais une suite de raccourcis (`u`, `d`…).
- Test : `TestPasteIntoInputs` (texte de 22 000 caractères compris).

### ⚪ Mineurs

**4. Fuite de goroutine à l'annulation de `SubscribeEvents`.**
- Reproduction : plus de 32 événements en attente, plus personne pour lire,
  puis annulation. Reproduit avec le faux démon (100 événements) et contre le
  vrai démon (15 conteneurs `zzdsprobeburst*`, 45 événements).
- Cause : l'envoi `out <- …` n'écoutait pas `ctx.Done()`. Sans effet
  aujourd'hui, le contexte de l'abonnement n'étant jamais annulé.
- Correction : envoi dans un `select` avec `ctx.Done()`.
- Tests : `TestSubscribeEventsCancelWithFullBuffer`,
  `TestLiveEventsBurstCancel` (intégration). Les deux échouaient avant.

**5. `wordWrap` débordait de l'écran.**
- Reproduction : erreur de pull avec une URL de registre ou une référence
  `@sha256:…` dans la confirmation, l'écran d'erreur ou la progression. Sur
  un écran de 60 colonnes, une ligne en faisait 162 et le terminal décalait
  tout le cadre.
- Cause : mesure en octets, et un mot plus large que la ligne restait entier.
  Le texte accentué se repliait aussi trop tôt.
- Correction : mesure en colonnes (`ansi.StringWidth`), mot trop long coupé
  net.
- Tests : `TestWordWrapColumns` (7 messages, dont 3 réels du démon, de 1 à
  120 colonnes), `TestWrappedViewsFitWidth` (les trois écrans qui replient).

**6. Collecte des métriques de l'hôte non sûre en concurrence.**
- Reproduction : `-race` sur les tests du faux démon, qui lancent plusieurs
  programmes à la suite dans un même processus. Le minuteur de métriques d'un
  programme terminé tournait encore pendant la collecte du suivant.
- Cause : `monitor.Collect` met à jour un état global (compteurs de la mesure
  précédente) sans verrou. En production, une seule collecte est en vol à la
  fois : le défaut n'y est pas atteignable aujourd'hui.
- Correction : `Collect` est sérialisé par un verrou, pris toutes les 2 s.
- Test : `TestCollectConcurrent` (4 goroutines × 20 collectes), signalé par
  `-race` sans le verrou.

## Optimisation : logs regroupés par image

bubbletea recalcule toute la vue après chaque message. Un conteneur bavard
ordinaire envoie ses lignes une à une, donc un rendu complet par ligne.
`readLogCmd` dort maintenant une image (50 ms, la cadence de rendu) après la
première ligne, puis vide le canal. Le canal de `LogsAsync` passe de 200 à
4 096 lignes d'avance, pour ne pas freiner compose pendant ce temps.

| Suivi réel d'une stack jetable, 20 s | Avant | Lot sur une image | Sommeil d'une image (retenu) |
|---|---|---|---|
| Débit maximal (≈ 47 000 lignes/s, limité par dockerd) | 169 % d'un cœur | 108 à 119 % | 85 % |
| ≈ 490 lignes/s | 40 % | 15 % | 11 % |

Ni perte ni retard dans aucun cas : le flux est en contre-pression, la TUI
suit le producteur à quelques lignes près. Le reste du coût à 490 lignes/s
vient du flux compose et moby (`stdcopy`, lecture HTTP) et de l'ordonnanceur
Go. Test : `TestLogLinesCoalescedPerFrame`, qui échouait avant (1 ligne par
lot).

## Mesures

### 5. Logs en suivi réel

Stack jetable qui écrit en boucle, journal limité à 2 × 20 Mo.

| Mesure | Résultat |
|---|---|
| Pertes | aucune (contre-pression, pas d'abandon) |
| Mémoire après 10 ouvertures et fermetures | goroutines 23 → 17, tas 4,4 → 5,4 Mo, descripteurs 10 → 8 |
| Défilement dans 10 000 lignes (frappe et rendu) | 0,21 ms |
| Tas pendant le suivi, 10 000 lignes gardées | 6 à 11 Mo |

### 6. Contre-pression de la progression

Pull réel de 4 images absentes (httpd, memcached, haproxy, rabbitmq en
alpine), 2 séries, images supprimées entre chaque passage.

| Série | Sans TUI, canal lu à vide | Avec la TUI (vrai programme, 20 images/s) |
|---|---|---|
| 1 | 5,96 s, 235 événements | 6,03 s, 238 événements, CPU 10 % |
| 2 | 5,99 s, 241 événements | 6,16 s, 237 événements, CPU 10 % |

Aucun événement « working » abandonné : compose v5 espace lui-même ses
événements de pull (≈ 40/s ici). La TUI ne ralentit pas le pull au-delà du
bruit du réseau. Le compteur `compose.DroppedWorking()` reste en place pour
refaire la mesure.

### 7. Vue de progression à 10 000 ressources

| Bloc | Rendu | Allocations |
|---|---|---|
| 3 000 ressources | 0,87 ms | 1 555 |
| 10 000 ressources | 0,93 ms | 1 555 |

Les fermetures de la passe de comptage restent sur la pile : aucune
allocation par ligne. Le comptage pèse ≈ 14 % d'une image à 10 000
ressources, soit 0,3 % d'un cœur à 20 images/s. Rien à corriger.

### 8. D'où viennent les réveils au repos

Trace d'exécution de 60 s du vrai binaire en pseudo-terminal, puis comptage
des changements de contexte par thread à plusieurs cadences de rendu.

| Cadence de rendu | Changements de contexte | dont `sysmon` |
|---|---|---|
| 1 image/s | 68 à 69 /s | 9 /s |
| 20 images/s (actuel) | 172 à 175 /s | 45 à 47 /s |
| 60 images/s | 407 /s | 123 /s |

- Chaque image coûte ≈ 5,5 réveils de threads : le minuteur du moteur de
  rendu (20 réveils de goroutine/s), les threads de l'ordonnanceur qui
  cherchent du travail, et `sysmon`, qui espace moins ses passages quand
  l'activité est régulière. Soit ≈ 105 réveils/s sur 173.
- Le reste, ≈ 68/s, ne dépend pas de la cadence : `sysmon` au plus calme
  (9/s), minuteurs applicatifs (≈ 3 réveils de goroutine/s : métriques,
  statistiques, liste), ramasse-miettes et attente réseau.
- Pendant un pull : ≈ 12 lots de progression/s et 13 lectures réseau/s en
  plus. Rien d'anormal.

La cadence de 20 images/s est gardée. La baisser à 10 économiserait ≈ 55
réveils/s, de l'ordre de 0,3 % d'un cœur, au prix d'un spinner et d'un
défilement moins fluides.

### 9. Démarrage dans un terminal qui répond

Le vrai binaire, 20 lancements en pseudo-terminal 190 × 50 sur les 63 vraies
stacks : 10 sans réponse aux requêtes, 10 avec un pilote qui répond comme
xterm. Le binaire n'envoie que trois requêtes au démarrage (sortie
synchronisée, mode Unicode, protocole clavier kitty), sans attendre leur
réponse.

| Terminal | Premier écran (médiane) | Liste chargée (médiane) | Lents (> 120 ms) |
|---|---|---|---|
| Sans réponse | 77,7 ms | 77,7 ms | 3 sur 10 |
| Répond comme xterm | 78,2 ms | 78,4 ms | 0 sur 10 |

Pas d'écart de médiane : le pseudo-terminal muet ne faussait pas les 75 ms
de la passe précédente. Les trois chargements à 126-132 ms tombent tous du
côté sans réponse, mais 10 lancements ne suffisent pas à établir un lien.

La prise de contact bornée de `compose.New` (ping avec négociation de
version) coûte 0,46 ms en médiane, sur 1,26 ms pour tout `compose.New`. Ce
n'est pas un coût ajouté : sans elle, le premier appel au démon négociait de
la même façon.

### 10. Sélecteur de thèmes

| Opération | Coût |
|---|---|
| Frappe dans la recherche `/` (filtre et rendu) | 1,95 ms |
| dont le filtre sur les 659 thèmes | 0,07 ms |
| `applyScheme` et la palette corrigée | 0,015 ms |
| Rendu du sélecteur seul | 1,85 ms |

Le rendu domine. À la vitesse d'une frappe humaine, c'est négligeable.

### 11. Goroutines dans la durée

Vrai `tea.Program` sur les 63 vraies stacks (lectures réelles : liste,
occupation, événements, logs ; actions simulées par un flux qui ne finit qu'à
l'annulation). 30 minutes au repos, puis 20 cycles : logs d'une stack réelle
ouverts puis fermés, opération lancée puis annulée (esc esc), `R`, puis `o`
et le même dossier.

| Moment | Goroutines | Tas | Descripteurs |
|---|---|---|---|
| Départ (liste chargée + 10 s) | 21 | 4,4 Mo | 9 |
| Repos 5 min | 19 | 4,6 Mo | 8 |
| Repos 30 min | 19 | 4,8 Mo | 8 |
| Après 5 cycles | 21 | 4,7 Mo | 8 |
| Après 20 cycles | 20 | 4,8 Mo | 8 |

Aucune fuite : tout revient à son niveau de départ.

## Robustesse

### 12. Démon qui tombe

Faux démon sur socket Unix (`internal/fakedocker`) : `/_ping`, `/info`,
`/containers/json`, inspection et logs d'un conteneur, `/events` en flux. Il
peut couper toutes ses connexions, refuser les requêtes en les comptant, se
taire, puis revenir. Vrai client, vrai `tea.Program`.

- Logs en suivi : le flux s'arrête et la dernière ligne explique « flux de
  logs interrompu ».
- Événements : 1 à 4 tentatives d'abonnement en 6 s, une toutes les 2 s. Pas
  de boucle chaude, ni là ni sur les autres routes.
- `R` pendant la panne : écran « Démon Docker injoignable » (bug 2).
- Retour du démon : `R` rétablit la liste, et l'abonnement aux événements
  reprend seul.
- Démon muet au lancement : écran d'erreur au bout de 10 s, puis `R` une fois
  le démon revenu recharge la liste.
- Tests : `TestDaemonDownAndBack`, `TestDaemonMuteThenR`, ≈ 16 s, sautés avec
  `-short`.

### 13. Système de fichiers

- Stack supprimée pendant l'affichage : le volet affiche « Lecture
  impossible », la bascule du compose plein écran affiche l'erreur, et un down
  échoue proprement (« no such file »). `TestStackDeletedWhileDisplayed`.
- Compose remplacé par un fichier invalide entre la prévision et la
  confirmation : l'up relit le fichier, échoue sur l'erreur YAML et
  n'applique pas l'ancien plan. `TestComposeReplacedBeforeConfirm`.
- Liens symboliques : un lien vers un dossier n'est pas suivi, comme
  `filepath.WalkDir` dont le scan est issu. Aucun lien de ce type dans
  `/home/ubuntu`. Conséquence : ni boucle ni doublon possible. Un fichier
  compose qui est lui-même un lien est pris. Comportement verrouillé par
  `TestScanSymlinks`, boucle et lien cassé compris. **Décision de
  l'utilisateur : on laisse ainsi.**
- Limite connue : une fois le dossier supprimé, la stack disparaît de la liste
  même si ses conteneurs tournent encore. dockstack ne peut plus les arrêter,
  la CLI le pourrait avec `docker compose -p <nom> down`.

### 14. cgroups

- Conteneur `zzdsprobecg` réel supprimé entre deux lectures : pas de
  panique, chemin oublié, l'API prend le relais puis renvoie son erreur.
  `TestLiveCgroupContainerRemoved`.
- `memory.current` absent : la lecture échoue et le conteneur passe par
  l'API. `TestCgroupMemoryCurrentMissing`.
- `io.stat` vide : débits nuls, CPU et mémoire lus. `TestCgroupEmptyIOStat`.
- **Non testable ici** : pilote cgroupfs, cgroups v1 et rootless. La machine
  n'a que cgroups v2 et le pilote systemd. Seules des arborescences factices
  les couvrent.

### 15. Clavier, souris, collage

`TestMonkeyAsync` complète `TestMonkeyKeys` : toutes les majuscules, la
molette, des collages (court, multiligne, 15 000 caractères, séquences
d'échappement) et un message asynchrone sur trois livré plus tard, dans le
désordre. Cela couvre les frappes pendant une capture, une prévision, un
chargement ou une opération. 200 graines × 400 événements, plus 200 graines
de `TestMonkeyKeys` : ni panique, ni écran débordant.

### 16. Fuzzing

| Cible | Propriétés vérifiées | Entrées (3 min) |
|---|---|---|
| `FuzzScanComposeDir` | arborescences générées ; un fichier par dossier, le plus prioritaire, rien d'oublié (référence `WalkDir`) | 136 000 |
| `FuzzLoadProjectDotEnv` | aucun `.env` ne fait paniquer `loadProject` | 485 000 |
| `FuzzSortByState` | rien de perdu, groupes contigus et triés, tri stable, état connu | 2 697 000 |
| `FuzzDiffCapture` | chaque entrée une fois, statuts triés, ordre de la capture gardé | 2 468 000 |

Aucune défaillance trouvée.

## Finitions

- **Documentation (17)** : `CONTRIBUTING.md` lance les tests d'intégration
  sur `./internal/compose/` et `./internal/tui/`, et documente
  `DOCKSTACK_MONKEY`, `DOCKSTACK_MEASURE`, `-short` et les 8 cibles de
  fuzzing.
- **CI (18)** : sous `-race`, en local sur 8 cœurs, le paquet `tui` passe
  de 41 s (ligne de base) à 103 s. `TestMonkeyKeys` prend 31 s,
  `TestMonkeyAsync` 36 s, les deux tests du faux démon 16,5 s. Sur un
  exécuteur GitHub à 4 cœurs, compter de l'ordre du double, loin de la limite
  de 10 minutes de `go test`. Si la CI devient trop lente, réduire les graines
  par défaut de `TestMonkeyAsync` est le levier le plus simple. Les graines des 8 cibles de fuzzing tournent
  bien comme des tests ordinaires (vérifié avec `go test -run '^Fuzz' -v`).
- **`cmd/debug` (19)** : 30 lignes qui listaient les conteneurs avec leur
  projet et leur dossier compose, l'équivalent d'un `docker ps -a --format`.
  Sans test ni documentation, pas compilé par GoReleaser. **Supprimé** à la
  demande de l'utilisateur.
- **Vérification à l'œil (20)** dans le vrai binaire, avec VHS, `HOME` isolé
  et un dossier de stacks jetables :
  - confirmation de Remove avec avertissement : tient en 80 colonnes, se
    replie proprement en 63 ;
  - titre et détail des captures en 80 colonnes : corrects ;
  - filtre du mode services : le mode exige 100 colonnes (message explicite en
    80). À 100 colonnes, la saisie longue montre sa fin précédée de « … » ;
  - en-tête « … » pendant le chargement, démon muet : correct, puis écran
    « Démon Docker injoignable ».

## Vérifications

- `gofmt -l .` vide, `go vet ./...` avec et sans `-tags integration`,
  `go test -race ./...` : vert. Les tests du harnais `tea.Program` passent
  3 fois de suite sous `-race`.
- Tests d'intégration réels : 18 dans `compose` (dont les nouveaux
  `TestLiveEventsBurstCancel` et `TestLiveCgroupContainerRemoved`) et
  `TestLiveJourney` : verts. Aucun conteneur `zzdsprobe*` restant, images de
  mesure supprimées.
- `govulncheck` (dernière version) : aucune vulnérabilité atteignable.
- Frappes aléatoires : 200 graines de `TestMonkeyKeys` et de
  `TestMonkeyAsync`, vertes.

## Pistes écartées

| Piste | Mesure | Pourquoi |
|---|---|---|
| Regrouper aussi la progression par image | ≈ 12 lots/s pendant un pull, 10 % d'un cœur au total | gain de l'ordre de 2 points, seulement pendant une opération |
| Baisser la cadence de rendu | ≈ 5,5 réveils par image | 0,3 % d'un cœur pour 10 images/s de moins, au prix de la fluidité |
| Comptage de la progression sans fermetures | aucune allocation, 14 % d'une image à 10 000 ressources | gain dans le bruit |
| Recherche de thèmes mémorisée | filtre 0,07 ms par frappe | négligeable devant le rendu |
| Suivre les liens vers des dossiers | — | refusé par l'utilisateur : aucun lien de ce type ici |

## Ce qui n'a toujours pas pu être vérifié

- **Redémarrage réel du démon** : simulé seulement par le faux démon.
- **cgroups v1, pilote cgroupfs, rootless, démon distant** : absents de la
  machine.
- **Terminal graphique réel** pour le démarrage : mesuré avec un
  pseudo-terminal qui imite les réponses d'xterm, pas avec un émulateur.
- **Coller dans un vrai terminal** : vérifié par messages `tea.PasteMsg`
  dans le programme, pas par un collage réel dans VHS.
- La CI GitHub n'a pas été lancée : les durées sous `-race` sont locales.

## Ajustements des tests existants

- Le harnais `drive` des tests abandonne tout message plus lent que 20 ms.
  Le lot de logs part maintenant après 50 ms : `TestLiveJourney` allonge ce
  délai (`quickWait`) pour la seule étape des logs.
- Le harnais `tea.Program` rend l'écran dans la boucle du programme. Le
  rendre depuis le test faisait courir les caches partagés du Model avec le
  rendu du programme (signalé par `-race`, artefact du test).

## Méthode

- **Mesures longues** : `internal/tui/measure_integration_test.go`
  (`DOCKSTACK_MEASURE=1`). Le Model tourne dans un vrai `tea.Program`, sortie
  jetée, rendu à 20 images/s (`harness_test.go`).
- **Trace et réveils** : un `main` temporaire, retiré depuis, enregistrait une
  trace avec `runtime/trace` et prenait la cadence dans `FPS`. Un pilote
  Python en pseudo-terminal (190 × 50) répondait aux requêtes du terminal et
  relevait les changements de contexte par thread dans `/proc`. La trace était
  ensuite analysée avec `go tool trace -d=parsed` : chaque réveil de goroutine
  est attribué à l'endroit où elle s'était bloquée.
- **Démon qui tombe** : `internal/fakedocker`.
