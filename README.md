# DockStack

TUI (terminal) en Go pour gérer des stacks Docker Compose : lister les stacks
détectées, les démarrer/arrêter/redémarrer/recréer, pull les images, suivre
les logs, et capturer/restaurer l'état des stacks en cours d'exécution.

![demo](demo.gif)

## ⚠️ Avertissement important

**Ce projet est entièrement vibecodé** : tout le code a été généré par un
agent IA (Claude / Claude Code), aucune ligne n'a été écrite à la main par un
développeur humain.

Je relis et corrige au fur et à mesure les bugs que je rencontre dans mon
usage quotidien.

**Utilisez ce projet à vos propres risques**, idéalement sur un environnement
où une erreur n'a pas de conséquence grave. Relisez le code vous-même avant
de l'utiliser sur des stacks importantes.

## Fonctionnalités

- Liste des stacks Docker Compose détectées, groupées par dossier, avec état
  (running / stopped / unhealthy) et compte de conteneurs
- Actions : Up, Stop, Down, Restart, Recreate, Pull, Remove (avec
  confirmation), Logs, avec les mêmes lettres dans la liste, le mode services
  et le panneau d'actions
- Actions groupées sur une sélection multiple ou un dossier entier
- CPU, RAM, disque et débits disque (lecture/écriture par seconde) sur la ligne
  de chaque stack, sous un en-tête de colonnes. CPU, RAM et débits : lecture des
  seuls conteneurs à l'écran, toutes les 3 s, directement dans les cgroups v2
  quand dockstack tourne sur l'hôte Docker (sans solliciter le démon), sinon
  via l'API (CPU et débits apparaissent à la
  seconde lecture, ce sont des écarts). Disque : volumes nommés + couches
  d'écriture des conteneurs, remesurés toutes les 10 min ; « — » = rien côté
  Docker, les bind mounts ne sont pas comptés. Écran étroit : les colonnes
  partent dans l'ordre débits, disque, CPU/RAM
- Volet de détail à droite de la liste : services (problèmes en premier) avec
  image et ports, détail du disque de la stack, actions adaptées ; `tab` bascule
  vers le fichier compose
- Gestion **service par service** (`→`, `←` pour sortir) : cocher des services, les
  filtrer (`/`), cocher ceux en défaut (`e`), puis stop / up / restart /
  recreate / pull / logs sans toucher aux autres. Image, ports et statut
  Docker du service sous le curseur ; avertissement si une dépendance est
  arrêtée (`+` pour l'inclure)
- Avant un `up` sur une stack en marche, prévision des services qui seront
  recréés (config modifiée, image mise à jour) ; rien à recréer = pas de
  confirmation
- Vue de progression en temps réel pendant les opérations, façon
  `docker compose` en CLI
- Captures d'état (`b`) : mémorisent quelles stacks **et quels services** tournent
  (jamais leurs données). Restauration sélective : seules les stacks à
  relancer sont cochées, les services arrêtés volontairement ne sont pas
  relancés. `i` met l'état actuel à l'identique d'une capture (démarre ce qui manque,
  arrête ce qui n'y figure pas, après confirmation). `n` nomme une capture,
  `*` l'épingle (jamais supprimée par la rotation). Un bandeau rappelle,
  au lancement, les stacks de la dernière capture qui ne tournent plus
- Page **Nettoyage** (`n`) : bilan façon `docker system df`, éléments
  supprimables classés par risque (cache de build, images pendantes, réseaux
  inutilisés d'abord ; images à retélécharger et volumes orphelins jamais
  cochés d'office) et commandes `prune` avec la sémantique exacte de la CLI.
  Une suppression qui efface des données (volumes, conteneurs arrêtés) se
  confirme avec `y` seulement, pas avec `↩`
- Mêmes règles de chargement que `docker compose` : `.env` du dossier,
  fichier d'override, `COMPOSE_PROJECT_NAME`, `COMPOSE_PROFILES` ; un up ou
  un recreate garde les volumes anonymes des conteneurs remplacés
- Filtrage / recherche dans la liste des stacks
- Thèmes de couleurs (`t`) : les 650+ thèmes livrés avec
  [Ghostty](https://ghostty.org) (Dracula, Nord, Gruvbox, Catppuccin,
  TokyoNight, Solarized…), clairs comme sombres, avec aperçu en direct sur
  toute l'interface. Les couleurs trop peu contrastées sont corrigées pour
  rester lisibles

## Prérequis

- Accès au socket Docker via le groupe `docker`
  (`sudo usermod -aG docker $USER`) — **ne pas lancer DockStack avec `sudo`** :
  `~/.dockstack` serait créé en root et les captures ne pourraient plus y être
  écrites. Le binaire utilise l'API `docker/compose/v5`, pas le binaire
  `docker compose`.
- Pour compiler depuis les sources : Go 1.26+

## Installation

Binaire statique Linux (amd64 ou arm64), sans dépendance :

```bash
curl -L https://github.com/Bloopps/DockStack/releases/latest/download/dockstack-linux-amd64 -o dockstack
chmod +x dockstack
./dockstack
```

Pour arm64, remplacer `dockstack-linux-amd64` par `dockstack-linux-arm64`.
`./dockstack --version` affiche la version installée.

## Lancer depuis les sources

```bash
go run ./cmd/dockstack
```

## Configuration

Fichier `~/.dockstack/config.json` (créé automatiquement au premier
lancement) :

```json
{
  "stack_dir": "/chemin/vers/vos/stacks",
  "max_parallel": 4
}
```

- `stack_dir` : dossier racine scanné pour trouver les fichiers
  `compose.yaml` / `compose.yml` / `docker-compose.yml` / `docker-compose.yaml`
  (un seul par dossier, dans cet ordre de priorité, comme la CLI)
- `max_parallel` : nombre d'opérations simultanées lors des actions groupées
  (défaut 4, aussi utilisé si la valeur est absente ou à 0)
- `show_all` : afficher aussi les stacks qui ne tournent pas, arrêtées ou
  jamais déployées (défaut `false` : seules celles qui tournent sont listées ;
  bascule avec `a`). Remplace l'ancien `show_inactive`, ignoré
- `exclude` : chemins relatifs à `stack_dir` ignorés par le scan, avec tout
  ce qu'ils contiennent (ajout avec `x` ; retirer l'entrée pour annuler),
  par ex. `["dockerstack", "tests/fixtures"]`
- `theme` : nom du thème de couleurs (choisi avec `t`). Absent = thème
  d'origine, qui reprend les couleurs du terminal

### Thèmes

Le sélecteur (`t`) applique le thème sous le curseur à toute l'interface ;
`↩` l'enregistre, `esc` revient au précédent. Il s'ouvre sur une sélection
d'une soixantaine de classiques (« populaires ») ; `tab` passe à tout le
catalogue, puis aux seuls sombres ou clairs, et `/` cherche un nom dans tout
le catalogue. Un thème impose au terminal son fond et sa
couleur de texte le temps de l'exécution (rétablis en sortie). Le rendu est
fidèle en couleurs 24 bits ; par SSH, penser à transmettre `COLORTERM=truecolor`
si le terminal le permet, sinon les couleurs sont approchées en 256.

Thèmes personnels : un fichier par thème, au format Ghostty, dans
`~/.dockstack/themes/` ou `~/.config/ghostty/themes/` (le nom du fichier est
celui du thème ; il remplace un thème intégré de même nom) :

```
background = #1e1e2e
foreground = #cdd6f4
selection-background = #585b70
selection-foreground = #cdd6f4
palette = 0=#45475a
palette = 1=#f38ba8
…
palette = 15=#a6adc8
```

Le catalogue intégré se met à jour avec `scripts/update-themes.sh`.

## Raccourcis principaux

| Touche                | Action                                              |
|-----------------------|-----------------------------------------------------|
| `u` / `s` / `d` / `r` / `c` / `p` | Up / Stop / Down / Restart / Recreate / Pull |
| `l`                   | Logs de la stack courante (suivi automatique en bas, `/` filtrer, `w` retour à la ligne, `pgup`/`pgdn` défiler) |
| `↩`                   | Panneau d'actions (mêmes lettres, plus Remove)       |
| `␣`                   | Sélectionner                                         |
| `ctrl+a`              | Tout sélectionner (visible)                          |
| `esc`                 | Désélectionner / fermer                              |
| `←` / `→`             | Sortir / entrer : dossier, ou services d'une stack   |
| `g` / `G`             | Haut / bas (liste, logs, progression, captures…)     |
| `/`                   | Filtrer                                              |
| `v`                   | Fichier compose en plein écran (`tab` : dans le volet) |
| `R`                   | Rafraîchir (auto sur événement Docker + toutes les 60 s) |
| `b`                   | Captures d'état (`c` capturer, `↩` relancer, `i` à l'identique, `n` nommer, `*` épingler, `del` ou `⌫` supprimer) |
| `a`                   | Afficher / masquer les stacks arrêtées et non déployées (masquées par défaut) |
| `x`                   | Exclure du scan la stack ou le dossier sous le curseur |
| `o`                   | Changer de répertoire                                |
| `n`                   | Nettoyage (images, cache de build, réseaux, volumes orphelins, `prune`) |
| `t`                   | Thème de couleurs (aperçu en direct, `↩` enregistre) |
| `?`                   | Aide, depuis n'importe quelle vue (`↑↓` pour défiler) |
| `q`                   | Quitter (deux frappes, depuis toutes les vues ; `esc` = retour) |

## Régénérer la démo

Le GIF en haut du README est généré avec [VHS](https://github.com/charmbracelet/vhs) :

```bash
go build -o dockstack ./cmd/dockstack
vhs demo.tape
```
