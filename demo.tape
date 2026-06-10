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
# Puis :
#   vhs demo.tape

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
