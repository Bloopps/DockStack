# Audit performance et optimisation (30 septembre 2026)

Audit de la consommation de dockstack, au repos et à l'usage, et de la charge
qu'il impose au démon Docker. Toutes les valeurs ont été **mesurées** sur la
machine de production, pas estimées.

## Résumé

L'application est saine : démarrage en moins de 80 ms, mémoire stable, rendu
d'un écran en 1,6 ms, aucune fuite de goroutines. Son coût se concentre sur
le **repos** : une instance ouverte consommait en permanence 4 à 5 % d'un cœur,
et en faisait consommer environ 3 % au démon Docker.

| # | Point | Gain | État |
|---|-------|------|------|
| 1 | Statistiques des conteneurs lues dans les cgroups | dockerd −2,5 pts, passage 32 → 8 ms | ✅ fait (28952cb) |
| 2 | Cadence de rendu 60 → 20 images/s | dockstack −1,7 pt (mesuré) | ✅ fait |
| 3 | Filtrer les événements Docker côté démon | ~2 réveils/s en moins | ✅ fait |
| 4 | Taille des images relue sur événement, pas toutes les 30 s | dockerd −0,4 pt | ✅ fait |
| 5 | Ne plus relire la table des montages toutes les 2 s | −13 Mo/min d'allocations | ✅ fait |
| 6 | Mise en page du rendu sans les assemblages lipgloss | rendu 1,69 → 0,81 ms | ✅ fait |

« pt » = point de pourcentage d'un cœur.

## Contexte des mesures

- Hôte : 8 cœurs, cgroups v2 (pilote systemd), Docker local.
- 63 stacks détectées sous `/home/ubuntu`, 15 en marche, 69 conteneurs
  (58 dans les stacks affichées), 38 contrôles de santé par minute.
- Terminal 190 × 50, thème Catppuccin Mocha, liste des stacks en marche.
- Code : `master` avant le point 1 (b08b315 + thèmes).

## Mesures de départ

### Au repos

| Mesure | Valeur |
|---|---|
| CPU de dockstack | 4,0 à 5,2 % d'un cœur selon les séries |
| CPU de dockerd causé par une instance | ≈ 3 % d'un cœur |
| Mémoire résidente | 40 Mo, stable |
| Threads / goroutines | 14 / 27, stables |
| Allocations | ≈ 133 Mo par minute |
| Octets écrits au terminal | ≈ 27 Ko par minute |

Répartition du CPU de dockstack au repos (profil pprof sur 60 s) :

| Poste | Part |
|---|---|
| Ordonnanceur et minuteurs Go, réveillés par le moteur de rendu à 60 images/s | ≈ 53 % |
| Rendu de la vue (`Model.View`) | ≈ 12 % |
| Lecture des statistiques via l'API (HTTP + JSON) | ≈ 10 % |
| Reste (diff du rendu terminal, GC, métriques système) | ≈ 25 % |

Répartition des allocations :

| Poste | Part |
|---|---|
| Rendu de la vue | ≈ 50 % |
| Décodage JSON des statistiques | ≈ 16 % |
| Relecture de `/proc/self/mounts` | ≈ 10 % |

### Démarrage

| Étape | Temps |
|---|---|
| Premier écran | 45 ms |
| Liste des stacks affichée | < 80 ms |
| Lecture du catalogue de thèmes | 1,1 ms |

Binaire : 43 Mo.

### Rendu (benchmarks, un écran complet)

| Élément | Temps | Allocations |
|---|---|---|
| Écran complet, 63 stacks | 1,60 ms | 359 Ko, 7 288 allocs |
| Écran complet, 600 stacks | 1,75 ms | 432 Ko, 9 991 allocs |
| En-tête | 0,22 ms | 29 Ko |
| Corps de la liste | 0,89 ms | 212 Ko |
| Volet de détail | 0,10 ms | 16 Ko |
| Sélecteur de thèmes | 1,87 ms | 404 Ko |
| Changement de thème | 11 µs | 0,7 Ko |

Le nombre de stacks pèse peu : seules les lignes visibles sont dessinées.
Dans un rendu, plus de la moitié du temps part dans la mise en page lipgloss
(`Style.Width/Height`, `JoinHorizontal`, `JoinVertical`, qui mesurent
plusieurs fois la largeur de tout l'écran) ; le dessin des lignes de stacks
n'en représente que ≈ 13 %.

### Appels Docker

| Appel | Fréquence | Durée côté dockstack | CPU de dockerd par appel |
|---|---|---|---|
| Statistiques des conteneurs (58) via l'API | toutes les 3 s | 30 ms | **74 ms** |
| `ListStacks` (liste des conteneurs) | 60 s + sur événement | 7,7 ms (19,5 ms à froid) | 21 ms |
| Occupation sans volumes | toutes les 30 s | 89 ms | 118 ms |
| dont `/system/df?type=image` | | | 115 ms |
| dont `/info` | | | 7 ms |
| Occupation avec volumes | toutes les 10 min | 308 ms | 547 ms |
| Prévision d'un up (`PlanUp`) | à la demande | 15 ms | |

Charge de dockerd par minute, pour une instance au repos :

| Source | CPU dockerd par minute |
|---|---|
| Statistiques (20 passages) | ≈ 1 470 ms |
| Taille des images (2 passages) | ≈ 230 ms |
| Taille des volumes (1 tous les 10 min) | ≈ 55 ms |
| Liste des conteneurs | ≈ 21 ms |
| **Total** | **≈ 1,8 s, soit ≈ 3 % d'un cœur** |

### Événements Docker

Le flux n'est filtré que par type (`container`). Sur une minute : 38
`exec_create`, 38 `exec_start` et 38 `exec_die`, tous produits par les
contrôles de santé, soit ≈ 2 événements par seconde. Chacun réveille la
boucle de l'application, lance une goroutine de lecture et provoque un rendu
complet, avant d'être ignoré par `IsRelevantDockerEvent`.

### Ce qui va bien

- `ListStacks` fait un seul appel au démon ; la recherche des fichiers
  compose est bornée à 3 niveaux et mise en cache 60 s.
- Les logs sont lus par lots, plafonnés à 10 000 lignes, et seules les lignes
  visibles sont dessinées.
- Les statistiques ne portent que sur les conteneurs proches de l'écran.
- Pas de fuite : goroutines, threads et mémoire stables dans le temps.

## Point 1 : statistiques lues dans les cgroups (fait)

Commit 28952cb. Sur l'hôte Docker en cgroups v2, les compteurs de chaque
conteneur sont lus dans son cgroup (`cpu.stat`, `memory.current`,
`memory.stat`, `io.stat`) avec les formules de `docker stats`, sans requête
au démon. Repli automatique sur l'API, conteneur par conteneur, si le cgroup
est introuvable (démon distant, cgroups v1, dockstack conteneurisé). Détails
dans `internal/compose/cgroup.go`.

| Mesure | Avant | Après |
|---|---|---|
| Passage sur 58 conteneurs | 32,5 ms | 7,7 ms |
| CPU dockerd par passage | 73 ms | 0 ms |
| CPU dockerd causé par une instance au repos | ≈ 3 pts | 0,44 pt |
| CPU de dockstack au repos | ≈ 5,1 % | ≈ 4,3 % |

Conformité vérifiée par `TestLiveCgroupMatchesAPI` (lecture seule) : 69/69
conteneurs trouvés, CPU cumulé, mémoire et disque conformes à l'API.

Les 0,44 pt restants côté démon viennent surtout de la taille des images
(point 4).

## Points 2 à 6 (faits)

### Résultat au repos

Même protocole que les mesures de départ (190 × 50, HOME isolé, fenêtres de
45 s), binaire d'avant (28952cb) et d'après en alternance, trois séries
chacun :

| Mesure | Avant | Après |
|---|---|---|
| CPU de dockstack | 4,40 / 4,40 / 4,49 % | 1,76 / 1,71 / 1,71 % |
| Réveils de dockstack (changements de contexte) | ≈ 441 /s | ≈ 170 /s |
| CPU de dockerd pendant la série | 2,82 / 2,18 / 2,38 % | 2,22 / 2,00 / 2,24 % |
| Octets écrits au terminal | ≈ 16 Ko/min | ≈ 16 Ko/min |

dockerd seul, sans aucune instance : 2,22 % puis 2,58 % (contrôles de santé
des autres conteneurs). La charge qu'une instance impose au démon est donc
désormais noyée dans ce bruit de fond : elle n'est plus mesurable avec ce
protocole.

### 2. Cadence de rendu

`tea.WithFPS(20)` dans `cmd/dockstack/main.go`. bubbletea réveillait son
moteur de rendu 60 fois par seconde même sans changement ; c'était le
premier poste de CPU au repos.

### 3. Événements filtrés par le démon

`compose.SubscribeEvents` passe au démon un filtre `event` (actions de
`relevantEventActions`, actions d'images, `health_status`) en plus de
`type=container,image`. Vérifié sur le démon 29.2 : `health_status` est
comparé par préfixe (l'action réelle `health_status: healthy` passe), et les
`exec_*` des contrôles de santé ne sont plus envoyés.
`IsRelevantDockerEvent` reste en garde-fou côté client et tient compte du
type. Test d'intégration `TestLiveEventFilter`.

### 4. Taille des images

`Usage` prend des `UsageOptions` (Images, Volumes) ; sans l'un ni l'autre,
`/system/df` n'est plus appelé. Le tick de 30 s ne lit plus que `/info`.
Les images sont relues :

- au premier passage, et avec les volumes toutes les 10 minutes (filet) ;
- 2 s après un événement qui peut les changer : image pull, delete, tag,
  untag, load, import, et création ou suppression d'un conteneur (la part
  libérable en dépend) ; le délai regroupe les rafales d'un up ;
- au passage suivant si une relecture a échoué.

Les dernières tailles connues sont conservées entre deux relectures.

### 5. Table des montages

`monitor.mountPoint` mémorise le point de montage par chemin et ne relit
`/proc/self/mounts` que si le périphérique du chemin change (`stat`) ou
toutes les 5 minutes.

### 6. Mise en page du rendu

`internal/tui/screen.go` : chaque ligne est mesurée une fois puis complétée
d'espaces à une largeur connue ; colonnes et empilement se font ligne par
ligne. Repli sur lipgloss si une ligne de la liste dépasse sa largeur (lipgloss
la replierait). `TestScreenMatchesLipgloss` compare le résultat à
l'assemblage lipgloss d'origine, octet par octet, sur 15 vues et 8 tailles.

| Écran complet, 63 stacks, 190 × 50 | Temps | Allocations |
|---|---|---|
| Assemblage lipgloss | 1,69 ms | 323 Ko, 8 060 allocs |
| Mise en page directe | 0,81 ms | 126 Ko, 2 156 allocs |

Benchmarks : `BenchmarkViewList` et `BenchmarkViewListLipgloss`.

## À l'échelle de 600 conteneurs

- Statistiques : avec les cgroups, coût côté démon nul et ≈ 0,13 ms par
  conteneur côté dockstack. Via l'API (repli), ≈ 1,3 ms de CPU dockerd par
  conteneur et par passage : ≈ 13 % d'un cœur pour 300 conteneurs affichés.
- Événements : filtrés par le démon, ils ne croissent plus avec les contrôles
  de santé, seulement avec les vrais changements d'état.
- Table des montages : croît linéairement avec les conteneurs, mais n'est
  plus relue qu'à un changement de périphérique ou toutes les 5 minutes.
- Rendu et liste : peu sensibles au nombre de stacks.

## Méthode, pour refaire les mesures

Toujours lancer l'instance de test avec un `HOME` isolé, contenant une copie
de `~/.dockstack/config.json`, pour ne jamais modifier la vraie config.

**CPU au repos.** Lancer le binaire dans un pseudo-terminal de 190 × 50,
attendre 8 s, puis lire `utime + stime` (champs 14 et 15 de
`/proc/<pid>/stat`) au début et à la fin d'une fenêtre de 45 s. Le même
relevé sur le PID de `dockerd`, avec et sans l'instance de test, donne la
charge imposée au démon.

```python
import os, pty, time, select, struct, fcntl, termios
T = os.sysconf("SC_CLK_TCK")
env = {"HOME": "/chemin/home-isole", "TERM": "xterm-256color", "LANG": "C.UTF-8", "PATH": os.environ["PATH"]}
pid, fd = pty.fork()
if pid == 0:
    os.execve("./dockstack", ["dockstack"], env)
fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 50, 190, 0, 0))
def drain(sec):
    end = time.time() + sec
    while time.time() < end:
        if select.select([fd], [], [], 0.2)[0]:
            os.read(fd, 65536)
def cpu(p):
    f = open(f"/proc/{p}/stat").read().rsplit(")", 1)[1].split()
    return (int(f[11]) + int(f[12])) / T
drain(8); a = cpu(pid); drain(45)
print(f"{(cpu(pid) - a) / 45 * 100:.2f} % d'un cœur")
os.write(fd, b"qq"); drain(1)
```

**Coût d'un appel pour le démon.** Relever le CPU de `dockerd` avant et après
N appels, par exemple :

```sh
D=$(pgrep -x dockerd)
cpu() { awk '{print ($14+$15)*10}' /proc/$D/stat; }   # en ms si CLK_TCK=100
a=$(cpu); for i in $(seq 10); do
  curl -s --unix-socket /var/run/docker.sock http://x/system/df?type=image >/dev/null
done; echo "$(( ($(cpu)-a)/10 )) ms par appel"
```

**Profil.** Un `main` temporaire qui appelle `pprof.StartCPUProfile`, lance
`tea.NewProgram(tui.New(cfg))`, puis écrit les profils CPU, allocations et
goroutines au bout de 60 s ; analyse avec `go tool pprof -top -cum`.

**Rendu.** Benchmarks de `Model.View()` sur un modèle de test (63 puis 600
stacks, largeur 190, volet affiché) avec `b.ReportAllocs()`.

**Événements.**

```sh
docker events --since 60s --until 0s --filter type=container --format '{{.Action}}' \
  | sed 's/:.*//' | sort | uniq -c
```
