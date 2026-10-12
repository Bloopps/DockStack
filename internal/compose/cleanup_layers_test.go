package compose

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"testing"

	"github.com/moby/moby/api/types/image"
)

// hist construit un historique (le plus récent d'abord, comme le démon) à
// partir d'entrées (instruction, taille) données de la plus ancienne à la
// plus récente.
func hist(entries ...any) []image.HistoryResponseItem {
	var out []image.HistoryResponseItem
	for i := 0; i+1 < len(entries); i += 2 {
		out = append([]image.HistoryResponseItem{{CreatedBy: entries[i].(string), Size: int64(entries[i+1].(int))}}, out...)
	}
	return out
}

func TestAlignLayerSizes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		layers int
		h      []image.HistoryResponseItem
		want   []int64
		ok     bool
	}{
		{"instructions sans couche écartées", 3,
			hist("ADD rootfs.tar /", 100, "ENV A=1", 0, "WORKDIR /app", 0, "COPY . .", 50, "CMD [\"x\"]", 0),
			[]int64{100, 0, 50}, true},
		{"ancien constructeur (#(nop))", 2,
			hist("/bin/sh -c #(nop) ADD file:abc in /", 100, "/bin/sh -c #(nop)  CMD [\"sh\"]", 0, "/bin/sh -c apk add x", 30),
			[]int64{100, 30}, true},
		{"COPY vide sans couche (buildkit) : repli sur les tailles non nulles", 2,
			hist("COPY a /", 10, "COPY vide /", 0, "COPY b /", 20),
			[]int64{10, 20}, true},
		{"indécidable", 3,
			hist("COPY a /", 10, "COPY vide /", 0, "COPY autre-vide /", 0, "COPY b /", 20),
			nil, false},
	} {
		got, ok := alignLayerSizes(tc.layers, tc.h)
		if ok != tc.ok || !slices.Equal(got, tc.want) {
			t.Errorf("%s : %v %v, want %v %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestChainIDs(t *testing.T) {
	// Définition OCI : ChainID(L0|L1) = sha256(ChainID(L0) + " " + DiffID(L1)).
	sum := sha256.Sum256([]byte("sha256:a sha256:b"))
	want := []string{"sha256:a", "sha256:" + hex.EncodeToString(sum[:])}
	got := chainIDs([]string{"sha256:a", "sha256:b"})
	if !slices.Equal(got, want) {
		t.Errorf("chaînes : %v, want %v", got, want)
	}
	// La même couche sur une autre base est une autre couche.
	if chainIDs([]string{"sha256:x", "sha256:b"})[1] == got[1] {
		t.Error("même couche sur deux bases : même identifiant")
	}
}

// L'exemple de l'explication : A et B partagent une couche de base que nulle
// autre image n'utilise. Supprimer A seule libère sa couche propre ; A et B
// ensemble libèrent aussi la base. Une base utilisée par une image gardée
// n'est jamais comptée.
func TestLayerIndexFreed(t *testing.T) {
	base, a1, b1, c0, c1 := "sha256:base", "sha256:a1", "sha256:b1", "sha256:c0", "sha256:c1"
	infos := map[string]layerInfo{
		"A": {size: 2100, diffIDs: []string{base, a1}, hist: hist("ADD base /", 2000, "RUN a", 100)},
		"B": {size: 2050, diffIDs: []string{base, b1}, hist: hist("ADD base /", 2000, "RUN b", 50)},
		// C : historique indécidable, mais ses couches ne servent qu'à elle.
		"C": {size: 700, diffIDs: []string{c0, c1}, hist: hist("COPY x /", 300, "COPY vide /", 0, "COPY vide2 /", 0, "COPY y /", 400)},
		// E : détail illisible.
		"E": {size: 900, shared: 100, err: errors.New("inspect")},
	}
	ix := buildLayerIndex(infos)
	check := func(ids []string, want int64, wantExact bool) {
		t.Helper()
		if got, exact := ix.Freed(ids); got != want || exact != wantExact {
			t.Errorf("Freed(%v) = %d (exact %v), want %d (exact %v)", ids, got, exact, want, wantExact)
		}
	}
	check([]string{"A"}, 100, true)
	check([]string{"B"}, 50, true)
	check([]string{"A", "B"}, 2150, true)
	check([]string{"C"}, 700, true) // lot exact
	check([]string{"E"}, 800, false)
	check([]string{"A", "B", "E"}, 2950, false)
	if !ix.Exact("C") || ix.Exact("E") {
		t.Error("précision par image")
	}

	// Une image gardée (D, en service) qui utilise la base : jamais libérée.
	infos["D"] = layerInfo{size: 2010, diffIDs: []string{base, "sha256:d1"}, hist: hist("ADD base /", 2000, "RUN d", 10)}
	ix = buildLayerIndex(infos)
	check([]string{"A", "B"}, 150, true)

	// Tailles contradictoires pour une même couche : elle devient inconnue, et
	// partagée, elle rend les images concernées approchées.
	infos = map[string]layerInfo{
		"A": {size: 2100, diffIDs: []string{base, a1}, hist: hist("ADD base /", 2000, "RUN a", 100)},
		"B": {size: 2050, diffIDs: []string{base, b1}, hist: hist("ADD base /", 1999, "RUN b", 51)},
	}
	ix = buildLayerIndex(infos)
	if ix.Exact("A") || ix.Exact("B") {
		t.Error("couche contradictoire partagée : images comptées exactes")
	}
}
