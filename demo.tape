# Génère demo.gif pour le README.
#
# Prérequis : VHS (https://github.com/charmbracelet/vhs) — sur Ubuntu :
#   curl -fsSL https://repo.charm.sh/apt/gpg.key | sudo gpg --dearmor -o /etc/apt/keyrings/charm.gpg
#   echo "deb [signed-by=/etc/apt/keyrings/charm.gpg] https://repo.charm.sh/apt/ * *" | sudo tee /etc/apt/sources.list.d/charm.list
#   sudo apt update && sudo apt install vhs
#
# Build le binaire avant l'enregistrement :
#   go build -o dockstack ./cmd/dockstack
#
# Accès Docker requis (sinon "Accès Docker refusé" dans la TUI) :
#   sudo usermod -aG docker $USER && newgrp docker
#
# La démo fait un vrai up/down sur demo-stacks/sample-app (nginx:alpine,
# projet "dockstack-demo-sample", aucun port/volume) : pull si l'image
# n'est pas déjà en cache, puis conteneur+réseau jetables supprimés par
# le down. Aucun lien avec les stacks réelles.
#
# Puis (depuis le shell où newgrp docker a été lancé) :
#   vhs demo.tape

# HOME isolé pour l'enregistrement : pointe vers demo-stacks/ (stacks
# d'exemple) au lieu du vrai répertoire de stacks de la machine.
# Voir demo-stacks/ et /tmp/dockstack-demo-home/.dockstack/config.json
Env HOME "/tmp/dockstack-demo-home"

Output demo.gif

Set Shell "bash"
Set FontSize 16
Set Width 1280
Set Height 800
Set Padding 10
Set TypingSpeed 60ms

Type "./dockstack"
Enter
Sleep 2s

# Parcourir la liste
Down
Sleep 300ms
Down
Sleep 300ms
Down
Sleep 800ms

# Ouvrir le panneau d'actions d'une stack
Enter
Sleep 2s
Escape
Sleep 500ms

# Démarrer (u) puis arrêter (d) une stack : sample-app, un nginx:alpine
# jetable et isolé (voir demo-stacks/sample-app), sans rapport avec les
# vraies stacks. Down x2 depuis jellyfin -> sample-app.
Down
Sleep 300ms
Down
Sleep 500ms
Type "u"
Sleep 5s
Enter
Sleep 800ms
Type "d"
Sleep 500ms
Type "y"
Sleep 3s
Enter
Sleep 500ms

# Filtrer
Type "/"
Sleep 300ms
Type "immich"
Sleep 1.5s
Escape
Sleep 500ms

# Quitter
Type "q"
Sleep 1s
