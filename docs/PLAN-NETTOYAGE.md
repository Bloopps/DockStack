# Plan : page « Nettoyage » (images, cache de build, réseaux, volumes)

Conception validée le 1er octobre 2026. Les quatre étapes sont faites (analyse, page, suppression, commandes prune).

## Constat sur la machine (lecture seule)

| Catégorie | Libérable | Risque |
|---|---|---|
| Cache de build (843 entrées) | ≈ 17 Go | aucun |
| Images (189, dont 116 pendantes `<none>`) | ≈ 24,5 Go | aucun pour les pendantes |
| Réseaux inutilisés | 4 | aucun, compose les recrée |
| Volumes non utilisés (25) | ≈ 0,9 Go | **données** |

Sur les 25 volumes non utilisés, 22 appartiennent à des stacks présentes sur
le disque mais arrêtées (authelia, rocketchat, formbricks…), 2 n'ont pas
d'étiquette (`jobdash-data`, `portainer_data`) et 1 seul est orphelin (`v3`).
Un `docker volume prune -a` effacerait donc de vraies données. Sur les 52
images inutilisées, 21 servent à des stacks arrêtées.

## Décisions de l'utilisateur

- **Page dédiée**, ouverte par `n` depuis la liste (touche libre).
- **Volumes** : seuls les orphelins (projet sans stack sur le disque) sont
  proposés, jamais cochés d'office. Les volumes d'une stack présente restent
  supprimables uniquement par son Remove.
- **Images des stacks arrêtées** : proposées, décochées par défaut.
- **Commandes `prune`** : disponibles aussi, avec la sémantique exacte de la
  CLI.

## Maquette

En tête de page, un bilan de l'espace utilisé, comme `docker system df`,
avec le disque qui porte le dossier de données de Docker (chiffres réels du
1er octobre). Après un nettoyage, il est remesuré et montre l'espace libéré.

```
🧹 Nettoyage                                 analysé il y a 3 s
 Disque /var/lib/docker  ████████████████░░  162 / 194 Go (89 %)
                                             23 Go libres
────────────────────────────────────────────────────────────────
                  Nombre  Actifs     Taille   Libérable
 Images              189      37    88,1 Go     24,5 Go
 Conteneurs           70      68     0,7 Go      0,0 Go
 Volumes              36      11     5,4 Go      0,9 Go *
 Cache de build      843       0    50,4 Go     17,0 Go
 Total                             144,5 Go     42,4 Go
 * Docker compte aussi les volumes des stacks arrêtées :
   seuls les orphelins sont proposés ci-dessous.
────────────────────────────────────────────────────────────────
 Sélection                                       cochée ≈ 18 Go
  Sans risque
  [x] Cache de build             843 entrées          17,0 Go
  [x] Images pendantes <none>    116 images           ≈ 1 Go
  [x] Réseaux inutilisés         4 réseaux              —
  À re-télécharger si besoin
  [ ] Images inutilisées         31 images            ≈ 6 Go
  [ ] Images de stacks arrêtées  21 images            ≈ 4 Go
  Données
  [ ] Volume orphelin            v3 (stack absente)    12 Mo
  Conservé : 22 volumes de stacks arrêtées, 2 sans étiquette

 Commandes prune (comme la CLI)
    docker system prune          conteneurs arrêtés, réseaux,
                                 images pendantes, cache
    docker image prune [-a]      pendantes [ou toutes inutilisées]
    docker builder prune [-a]
    docker network prune
    docker container prune       2 conteneurs arrêtés
    docker volume prune          anonymes seulement : 0
────────────────────────────────────────────────────────────────
 ␣ cocher · → détail · ↩ nettoyer · esc retour
```

Chiffres indicatifs. `→` sur une ligne déplie le détail (noms, tailles,
stack d'origine).

## Principes

1. **Classement par risque.** Sans risque coché d'office ; à re-télécharger
   et données jamais.
2. **Ce qui est affiché est ce qui est supprimé.** La sélection supprime par
   identifiant (`ImageRemove`, `NetworkRemove`, `VolumeRemove`,
   `BuildCachePrune`), sans `force` : si un élément se remet à servir entre
   l'analyse et la confirmation, Docker refuse et la progression le signale.
3. **Commandes prune** : appels `ContainerPrune`, `ImagePrune`,
   `NetworkPrune`, `BuildCachePrune`, `VolumePrune` du client, donc le même
   résultat que la CLI. Chacune affiche avant confirmation ce qu'elle
   supprimerait. `docker volume prune -a` est exclu (il effacerait les
   données des stacks arrêtées), sauf décision contraire.
4. **Bilan de l'espace** en tête de page : mêmes colonnes et mêmes chiffres
   que `docker system df` (parité CLI), plus le disque du dossier de données
   Docker. Remesuré après chaque nettoyage, avec l'espace libéré et le
   nouveau taux d'occupation du disque.
5. **Mêmes mécanismes que le reste de l'app** : confirmation (`viewConfirm`)
   avec la liste et le total, exécution dans la vue de progression (une ligne
   par élément, « Removed »), espace réellement libéré affiché à la fin.
6. **Analyse à l'ouverture seulement**, annulable : mesurer les volumes coûte
   plus d'une seconde au démon. Résultat gardé tant que la page est ouverte,
   `R` relance.
7. **Classement des volumes** : étiquette `com.docker.compose.project`
   comparée aux stacks du dossier scanné (nom de projet résolu, `.env`
   compris). Sans étiquette = inconnu, conservé.
8. **Images d'une stack arrêtée** : images référencées par un compose du
   dossier, résolues comme compose (variables du `.env`).

## Étapes proposées

1. Analyse et classement (paquet `compose`), testés avec le faux démon
   `internal/fakedocker` et contre le vrai démon sur des objets `zzdsprobe*`.
2. Page : bilan de l'espace, cases à cocher, détail, maquette validée en VHS avant de figer le
   visuel.
3. Exécution par la vue de progression, confirmation, espace libéré.
4. Section des commandes prune.

## Avancement

### Étape 1 : analyse et classement (faite)

`internal/compose/cleanup.go` : `Client.AnalyzeCleanup(ctx, stackDir)` lit
au démon `DiskUsage` (tout, détaillé), la liste des conteneurs et des
réseaux, charge chaque stack du dossier comme compose, puis
`classifyCleanup` classe sans toucher au démon.

| Catégorie | Risque | Règle |
|---|---|---|
| Cache de build | aucun | part libérable du démon, un seul élément |
| Image pendante | aucun | sans étiquette, aucun conteneur (même arrêté) |
| Réseau inutilisé | aucun | local, non prédéfini, aucun conteneur, pas externe d'une stack |
| Image inutilisée | retéléchargement | étiquetée, aucun conteneur, citée par aucun compose |
| Image de stack arrêtée | retéléchargement | aucun conteneur, citée par un compose (étiquette ou empreinte) |
| Volume orphelin | données | aucun conteneur, projet sans stack dans le dossier |
| Volume anonyme | données | aucun conteneur, anonyme |

Conservés et jamais proposés : volumes d'une stack présente, volumes sans
étiquette, tout ce qu'un conteneur utilise, et ce dont le décompte est
inconnu. Une stack illisible garde son nom de projet par défaut, pour que
ses volumes ne passent pas pour orphelins.

Tests : `TestClassifyCleanup`, `TestCollectStackRefs` (`.env`, `name:`,
service construit, réseau externe, compose illisible),
`TestNormalizeImageRef`, `TestCleanupRisk` ; contre le vrai démon,
`TestLiveAnalyzeCleanup` (un objet `zzdsprobe*` par catégorie, tout
supprimé après) et `TestLiveCleanupDFParity` (bilan identique à
`docker system df`).

Sur le vrai dossier `/home/ubuntu`, l'analyse retrouve le relevé manuel :
22 volumes de stacks conservés, `jobdash-data` et `portainer_data`
conservés, un seul volume orphelin, `v3_netbird_management_v3` (70 Mo,
probablement une ancienne copie de netbird).

### Tailles d'images exactes (fait)

Docker se contredit : son résumé annonce 24,5 Go libérables, son détail par
image 63,5 Go. Aucun des deux n'est juste, car les couches sont partagées :
supprimer deux images libère aussi les couches qu'elles sont seules à
partager.

`internal/compose/cleanup_layers.go` reconstitue chaque couche (identifiant
de chaîne OCI, taille tirée de l'historique de l'image) et calcule l'espace
libéré par n'importe quelle sélection (`LayerIndex.Freed`) :

- l'historique ne distingue pas une couche vide d'une instruction sans
  couche : on écarte les instructions qui n'en créent jamais (`ENV`, `CMD`…),
  à défaut les entrées de taille nulle ;
- une couche partagée doit avoir la même taille dans toutes ses images ;
- les couches restées inconnues d'une image, si nulle autre ne les utilise,
  forment un lot dont la taille est exacte (taille de l'image moins ses
  couches connues). Sinon l'image est estimée (« ≈ »).

Sur la machine : 189 images, toutes détaillées exactement, analyse complète
en 0,56 s. Prouvé contre le vrai démon (`TestLiveLayersFreedExact`) : trois
images jetables partageant une couche, espace prévu égal à l'octet près à
celui que le démon libère réellement (11 534 336 octets).

| Sélection d'images sur la machine | Libéré exact |
|---|---|
| Images pendantes | 29,1 Go |
| + images inutilisées | 36,5 Go |
| + images de stacks arrêtées | 72,7 Go |

### Étape 2 : la page (faite)

`internal/tui/cleanupview.go`, touche `n` depuis la liste (aussi dans
l'aide). Bilan (disque, colonnes de `docker system df`, images en exact),
sélection par sections de risque, catégories repliables (`→`/`←`), cases
`[x]`/`[-]`/`[ ]`, total de la sélection recalculé en exact, éléments
conservés, `R` réanalyse, `esc` revient (une analyse en vol est annulée et
son résultat écarté). Les images pendantes affichent leur origine quand
compose l'a posée (« ancien build kitchen_planner/api »).

Tests : `TestCleanupPageDefaults`, `TestCleanupToggleAndExpand`,
`TestCleanupStaleResultIgnored`, `TestCleanupError`, et la page dans
`TestViewsSurviveAnySize` (1 × 1 à 300 × 100). Vérifiée à l'œil dans le
binaire réel sur les vraies stacks (VHS, 80 et 120 colonnes, lecture seule).

### Étape 3 : la suppression (faite)

`internal/compose/cleanup_run.go` : `Client.CleanupLive(ctx, items)` supprime
exactement les éléments choisis, sur le flux de progression habituel (une
« stack » par catégorie, une ligne par élément, 4 suppressions simultanées).

- Jamais de `force`. Une image ou un volume remis en service depuis
  l'analyse est refusé par le démon et reste en place.
- Image à plusieurs étiquettes : chaque étiquette est retirée, la dernière
  supprime l'image. Docker accepte de retirer une étiquette d'une image en
  service tant qu'il en reste une : on vérifie donc d'abord qu'aucun
  conteneur ne l'utilise, sinon l'image resterait amputée de ses étiquettes.
- Cache de build : `BuildCachePrune` avec `All` (comme
  `docker builder prune -a`), le compte rendu du démon s'affiche.

Dans la page : `↩` ouvre la confirmation (détail par catégorie, espace
prévu, avertissement si des images devront être retéléchargées ou si des
données seront effacées, volumes nommés), `esc` y revient à la page.
Validée, la suppression passe par la vue de progression ; en la quittant, on
revient sur la page, réanalysée, avec « ✔ Libéré X · prévu Y » (tailles
totales mesurées par le démon avant et après).

Tests : `TestLiveCleanupRemoves` (vrai démon, objets jetables, refus d'une
image et d'un volume remis en service ; échoue sans la vérification des
étiquettes), `TestCleanupLiveBuildCache` (faux démon : le vrai cache n'est
jamais vidé par les tests), `TestCleanupRunFlow`,
`TestCleanupConfirmWarnings`, et `TestLiveCleanupEndToEnd` : vrai
`tea.Program`, vraie analyse, vraie suppression d'objets jetables, retour
avec « ✔ Libéré 2 Mo · prévu 2 Mo ». Une garde fait échouer ce test, sans
rien supprimer, si un élément hors des objets jetables arrivait jusqu'à la
suppression. Les harnais qui utilisent le vrai client (`TestLiveJourney`,
mesures) simulent la suppression.

### Étape 4 : les commandes prune (faite)

`internal/compose/cleanup_prune.go` : `Client.PruneLive(ctx, cmd)` appelle
le démon exactement comme la CLI (`ImagePrune` avec `dangling=true` ou
`false` pour `-a`, `BuildCachePrune` avec ou sans `All`, `NetworkPrune`,
`ContainerPrune`, `VolumePrune` sans `All`). `docker system prune` enchaîne
les étapes de la CLI : conteneurs arrêtés, réseaux inutilisés, images
pendantes, cache pendant. `docker volume prune -a` n'est pas proposé.

Dans la page, une section liste les 8 commandes avec, pour chacune, ce
qu'elle supprimerait d'après l'analyse et l'espace prévu (« ? » pour le
cache pendant, imprévisible). Un « ! » signale une commande qui va plus loin
que la sélection prudente : réseau externe d'une stack (son up échouera),
conteneurs arrêtés de stacks (à recréer), images de stacks arrêtées
(à retélécharger), volumes anonymes (données). `↩` sur une commande ouvre sa
confirmation avec ces avertissements, puis la progression (une ligne par
étape, compte rendu du démon), puis le retour sur la page réanalysée.

Tests : `TestPruneLive` (faux démon uniquement : routes, filtres, ordre des
étapes de `system prune`, comptes rendus), `TestCleanupPrunePreview`,
`TestCleanupPruneFlow`, `TestCleanupPruneLabelWithoutSize`. Aucun prune
n'est lancé sur le vrai démon par les tests : les harnais à vrai client le
simulent ou le refusent (garde du bout en bout). Vérifié à l'œil dans le
binaire réel (confirmation de `docker container prune` ouverte puis
annulée : « conteneurs arrêtés de sim-megastack »).

### Mise en page (après retour sur le vrai rendu)

- À partir de 140 colonnes, la sélection est à gauche et le bilan, avec ce
  qui est conservé, à droite : sur l'écran de l'utilisateur tout tient sans
  défilement. En dessous, disposition en colonne, inchangée.
- Une seule colonne de tailles pour les catégories, les éléments et les
  commandes prune (`TestCleanupSizesAligned`).
- Sur écran large, la colonne de droite décrit, sous le bilan, la ligne
  sous le curseur : une catégorie (ce qu'elle est, ce que coûte sa
  suppression, ses plus gros éléments), un élément (identifiant, espace
  libéré seul, âge, stack ou projet, étiquettes), une commande prune (ce
  qu'elle supprimerait, conteneurs et réseaux nommés, avertissements en
  entier). « Conservé » suit s'il reste de la place
  (`TestCleanupDetailPanel`).
- Textes allégés à la demande de l'utilisateur (« beaucoup de
  commentaires ») : plus de paragraphes explicatifs dans le panneau (les
  titres de section disent déjà le risque), titres courts (« Données »,
  « Commandes prune », « Conservé »), note du tableau réduite. Seuls les
  avertissements des commandes prune restent rédigés.
- La liste fenêtrée signale ce qui est caché : « ··· N au-dessus »,
  « ··· N en dessous » (`TestWindowWithMarks`, `TestCleanupLayouts`).

### Points ouverts

- Échecs intermittents des tests réels (résolu) : `go test ./...` lance
  les paquets en parallèle, et le nettoyage des objets `zzdsprobe*` en début
  de test d'un paquet supprimait ceux qu'un autre paquet venait de créer.
  `fakedocker.LiveLock` sérialise désormais ces tests par un verrou de
  fichier ; la commande parallèle passe deux fois de suite.
- En-tête de la liste : il affiche maintenant le libérable exact (même
  chiffre que la page). Les couches de chaque image sont gardées en mémoire
  par identifiant : seules les nouvelles images sont lues au démon. Mesure
  des images : 155 ms la première fois, 97 ms ensuite (le coût de la mesure
  Docker existante). « ≈ » si une image n'a pas pu être détaillée.
- Unités : la page suit l'en-tête (Go = 2³⁰ octets), `docker system df`
  compte en Go décimaux (88,06 GB = 82,0 Go).
