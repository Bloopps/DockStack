# Prompt : passe complémentaire (angles morts de la passe du 1er octobre)

> À coller tel quel dans une nouvelle session Claude Code ouverte dans
> `/home/ubuntu/dockerstack`.

---

Tu reprends la passe performance et débogage de dockstack du 1er octobre 2026
pour traiter **ce qu'elle a omis, survolé ou laissé non vérifié**. Ce n'est
pas une nouvelle passe complète : ne refais pas ce qui est déjà fait et
mesuré.

Réponds et rédige en français.

## 1. À lire d'abord

- `docs/PROMPT-PASSE-PERF-DEBUG.md` : le prompt de la passe précédente. Ses
  **règles non négociables (section 2) s'appliquent telles quelles**, en
  particulier : jamais d'écriture dans `~/.dockstack`, aucune action compose
  réelle sur les stacks existantes (seulement des projets jetables
  `zzdsprobe*`), aucune commande qui arrête ou redémarre le démon Docker de la
  machine, pas de commit sans demande, commandes de plus de 30 s en tâche de
  fond avec un fichier de log. **Ne jamais attendre au premier plan la fin
  d'une tâche de fond** : avance sur autre chose et laisse la notification
  arriver.
- `docs/AUDIT-PERF-DEBUG-2026-10-01.md` : le rapport de la passe, surtout
  « Ce qui n'a pas pu être vérifié » et « Pistes écartées ».
- `git status` : tout le travail des passes du 30 septembre et du 1er octobre
  est **non commité**. Ne fais ni `stash`, ni `reset`, ni `checkout`.

## 2. Ligne de base

Avant toute modification : `gofmt -l .`, `go vet ./...`,
`go test -race ./...`, puis en tâche de fond
`go test -tags integration -count=1 ./internal/compose/ ./internal/tui/`.
Note tout échec avant de toucher au code.

## 3. Angles morts à traiter

Pour chaque point : reproduire, chiffrer ou démontrer, corriger si c'est un
bug (avec un test qui échoue avant et passe après), sinon expliquer pourquoi
le point est sans objet.

### A. Défauts repérés mais non corrigés

1. **Prévision d'un up périmée.** `handlePlanUp` (`internal/tui/confirm.go`)
   ne vérifie que la vue d'origine. Scénario : `u` sur une stack en marche,
   démon lent, aller dans l'aide puis revenir à la liste avant la réponse. Le
   up part alors sans nouvelle frappe. Décider avec un numéro de séquence,
   comme `progressSeq`, si la demande doit être écartée.
2. **Envoi bloquant dans `SubscribeEvents`** (`internal/compose/client.go`) :
   `out <- DockerEvent{…}` n'écoute pas `ctx.Done()`. C'est sans effet
   aujourd'hui, car le contexte n'est jamais annulé, mais une fuite de
   goroutine se produit si plus de 32 événements attendent quand on annule.
   Reproduire en provoquant une rafale avec des conteneurs `zzdsprobe*`, puis
   corriger.
3. **`wordWrap`** (`internal/tui/progressview.go`) mesure en octets et ne coupe
   pas un mot plus large que la ligne (référence d'image, chemin). Il sert aux
   erreurs de la progression, à l'écran d'erreur et à la confirmation. Vérifier
   les débordements avec des messages réels de compose, et le texte accentué
   ou en caractères larges.
4. **`truncate` passé à `ansi.Truncate`** : relire chacun de ses appelants.
   Certains passaient-ils une chaîne déjà stylée, dont le comportement a
   changé ? Le rendu par défaut reste-t-il identique octet pour octet partout
   ailleurs que pour les caractères larges ?

### B. Mesures annoncées par le prompt précédent mais non faites

5. **Logs en suivi réel** : débit maximal soutenu sur une stack jetable
   bavarde (`zzdsprobe*`, `yes` ou une boucle `echo`). Mesurer le CPU, les
   pertes, la mémoire après 10 ouvertures et fermetures, et le coût du
   défilement sur 10 000 lignes.
6. **Contre-pression de la progression** : avec le regroupement par lots,
   combien d'événements « working » sont encore abandonnés pendant un vrai
   pull de plusieurs images (projet jetable) ? Un envoi terminal bloquant
   ralentit-il l'opération compose elle-même ? Mesurer la durée du pull avec
   et sans la TUI, en lisant le canal à vide.
7. **Allocations de la vue de progression** quand le bloc dépasse l'écran : la
   passe de comptage crée une fermeture par ligne. Benchmark à 10 000
   ressources ; ne corriger que si le gain sort du bruit.
8. **`go tool trace`** sur 60 s au repos puis pendant un pull jetable : d'où
   viennent les ≈ 175 réveils par seconde (minuteur de rendu, `sysmon`,
   minuteurs applicatifs) ? Chiffrer chaque source. Ne baisse pas les 20
   images/s sans le justifier.
9. **Démarrage dans un vrai terminal** : le pseudo-terminal de mesure ne
   répond pas aux requêtes du terminal, ce qui fausse peut-être les 75 ms.
   Mesurer avec VHS, ou un terminal qui répond, et donner aussi le surcoût de
   la négociation de version ajoutée dans `compose.New`.
10. **Sélecteur de thèmes** : coût de la recherche `/` sur les 659 thèmes à
    chaque frappe, et d'`applyScheme`.
11. **Goroutines côté TUI dans la durée** : instance laissée 30 min au repos,
    puis 20 cycles d'ouverture et fermeture des logs, d'opération annulée, de
    `R` et de `o`. Comparer les goroutines, le tas et les descripteurs
    ouverts (`/proc/<pid>/fd`) au début et à la fin.

### C. Robustesse non exercée

12. **Démon qui tombe en cours de route**, sans toucher au vrai démon :
    simuler avec un faux serveur HTTP sur socket Unix qui répond normalement
    (`/_ping`, `/info`, `/containers/json`, `/events` en flux), puis coupe les
    connexions. Vérifier le réabonnement aux événements (pas de boucle
    chaude), la fin du suivi de logs, l'écran d'erreur, puis le retour à la
    normale quand le faux démon revient. Vérifier aussi `R` depuis l'écran
    d'erreur après la correction du démon muet.
13. **Système de fichiers** : stack supprimée pendant l'affichage (volet,
    compose plein écran, action), lien symbolique vers un dossier de stack (non
    suivi aujourd'hui : est-ce voulu ?), boucle de liens, fichier compose
    remplacé pendant une prévision.
14. **cgroups** : conteneur qui disparaît entre deux lectures (test
    d'intégration avec un `zzdsprobe*` supprimé en cours de route), compteur
    `memory.current` absent, `io.stat` vide. Les pilotes cgroupfs, cgroups v1
    et rootless restent non testables ici : le dire.
15. **Clavier** : la passe de frappes aléatoires n'envoie que G, Q, R et T en
    majuscules. Ajouter toutes les majuscules (verrouillage des majuscules), la
    souris (molette dans la progression et le compose), le collage d'un long
    texte dans les filtres, et des frappes pendant une capture en cours.
16. **Fuzzing manquant** : détection et priorité des fichiers compose
    (`composeRank`, `scanComposeDir` sur des arborescences générées),
    `aggregateState` et le tri `sortByState` (propriétés : ordre stable,
    rien de perdu), `diffCapture`. Le `.env` relève de compose-go, en
    dépendance tierce : ne le fuzzer que pour vérifier que dockstack ne
    panique pas sur ses erreurs.

### D. Finitions laissées de côté

17. **Documentation** : `CONTRIBUTING.md` ne lance les tests d'intégration que
    sur `./internal/compose/`. Le parcours `TestLiveJourney` est dans
    `./internal/tui/`. Documenter aussi `DOCKSTACK_MONKEY` et les cibles de
    fuzzing.
18. **CI** : durée de `TestMonkeyKeys` sous `-race` dans la CI. Les graines de
    fuzzing tournent-elles bien comme des tests ordinaires ?
19. **`cmd/debug/main.go`** : petit outil qui liste les conteneurs, sans doc ni
    test. Est-il encore utile ? Le signaler sans le supprimer, la décision
    revient à l'utilisateur.
20. **Vérification à l'œil** dans le vrai binaire (VHS, HOME isolé, touches
    sans effet sur les stacks : jamais `y` ni ↩ dans une confirmation) des
    corrections d'affichage non vues : confirmation avec avertissement replié
    en 80 colonnes, titre des captures en 80 colonnes, filtre du mode services,
    en-tête « … » pendant le chargement.

## 4. Hors périmètre

- Le cache des services déclarés, invalidé par la seule date du compose :
  l'utilisateur a refusé d'y toucher.
- Les points 1 à 6 de l'audit du 30 septembre et les 4 optimisations du
  1er octobre, sauf régression constatée.
- Toute action sur le vrai démon (arrêt, redémarrage, réglages).

## 5. Livrables

1. Une section « Passe complémentaire » ajoutée à
   `docs/AUDIT-PERF-DEBUG-2026-10-01.md`, avec le même format : bugs classés
   🔴/🟡/⚪ avec reproduction, cause, correctif et test ; mesures avant/après ;
   pistes écartées ; ce qui n'a toujours pas pu être vérifié.
2. Les corrections dans l'arbre de travail, chacune couverte par un test.
3. Un récapitulatif final court. Ne dis « vérifié » que pour ce qui a été
   réellement exécuté.
