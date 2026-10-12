package tui

import (
	"fmt"
	"testing"
	"time"

	"github.com/Bloopps/dockstack/internal/compose"
)

// Benchmarks des vues autres que la liste : progression chargée, logs en
// suivi, sélecteur de thèmes, aide, captures, mode services.

// progressModel : vue de progression d'un pull de stacks nombreuses avec
// beaucoup de couches (≈ 3 000 ressources).
func progressModel(stacks, services, layers int) Model {
	m := scaleModel(10)
	m.view = viewProgress
	m.progress = newProgressState()
	m.progressTitle = "Pull"
	for s := range stacks {
		name := fmt.Sprintf("stack-%02d", s)
		for v := range services {
			img := fmt.Sprintf("Image %s-svc%d", name, v)
			m.progress.apply(compose.ProgressEvent{StackName: name, Container: img, Status: "Pulling", State: "working"})
			for l := range layers {
				m.progress.apply(compose.ProgressEvent{StackName: name, Container: fmt.Sprintf("%012x", s*10000+v*100+l),
					ParentID: img, Status: "Downloading", Details: "[==>      ] 12.3MB/45.6MB", Percent: 27, State: "working"})
			}
		}
	}
	return m
}

func BenchmarkProgressView(b *testing.B) {
	m := progressModel(10, 10, 30)
	b.ReportAllocs()
	for b.Loop() {
		_ = m.viewContent()
	}
}

// Débit de la vue de progression : un événement de couche appliqué (Update)
// puis le rendu, comme en plein pull.
func BenchmarkProgressEvent(b *testing.B) {
	m := progressModel(10, 10, 30)
	ev := progressEventMsg{evs: []compose.ProgressEvent{{StackName: "stack-03", Container: fmt.Sprintf("%012x", 30000+5*100+7),
		ParentID: "Image stack-03-svc5", Status: "Downloading", Details: "[====>    ] 20.1MB/45.6MB", Percent: 44, State: "working"}},
		seq: m.progressSeq}
	b.ReportAllocs()
	for b.Loop() {
		out, _ := m.Update(ev)
		_ = out.(Model).viewContent()
	}
}

// Suivi de logs : un lot de 256 lignes reçu alors que le tampon est plein.
func BenchmarkLogBatch(b *testing.B) {
	m := scaleModel(10)
	m.view = viewLogs
	m.logSeq = 1
	for i := range maxLogLines {
		m.logLines = append(m.logLines, fmt.Sprintf("[web-1] ligne %d", i))
	}
	batch := make([]compose.LogEntry, logBatchMax)
	for i := range batch {
		batch[i] = compose.LogEntry{Name: "web-1", Line: fmt.Sprintf("%s GET /api/v1/items?id=%d 200 12ms \"Mozilla/5.0\"", time.Now().Format(time.RFC3339), i)}
	}
	msg := logLinesMsg{entries: batch, seq: 1}
	b.ReportAllocs()
	for b.Loop() {
		out, _ := m.Update(msg)
		m = out.(Model)
	}
}

func BenchmarkLogsView(b *testing.B) {
	m := scaleModel(10)
	m.view = viewLogs
	for i := range maxLogLines {
		m.logLines = append(m.logLines, fmt.Sprintf("[web-1] ligne %d GET /api/v1/items 200", i))
	}
	m.logScroll = maxLogLines - m.logsHeight()
	b.ReportAllocs()
	for b.Loop() {
		_ = m.viewContent()
	}
}

func benchView(b *testing.B, set func(*Model)) {
	m := scaleModel(63)
	set(&m)
	b.ReportAllocs()
	for b.Loop() {
		_ = m.viewContent()
	}
}

func BenchmarkViewTheme(b *testing.B) {
	benchView(b, func(m *Model) { *m = openTheme(b, *m) })
}
func BenchmarkViewHelp(b *testing.B) {
	benchView(b, func(m *Model) { m.view = viewHelp })
}
func BenchmarkViewBackup(b *testing.B) {
	benchView(b, func(m *Model) { m.view = viewBackup })
}
func BenchmarkViewServices(b *testing.B) {
	benchView(b, func(m *Model) { m.svcFocus = true })
}
func BenchmarkViewComposePane(b *testing.B) {
	benchView(b, func(m *Model) { m.previewCompose = true })
}

// Changement de thème dans le sélecteur (↓ = aperçu du thème suivant).
func BenchmarkThemeStep(b *testing.B) {
	m := scaleModel(63)
	m = openTheme(b, m)
	b.ReportAllocs()
	for b.Loop() {
		out, _ := m.Update(keyMsg("down"))
		_ = out.(Model).viewContent()
	}
}

// openTheme ouvre le sélecteur de thèmes par la vraie boucle Update.
func openTheme(b *testing.B, m Model) Model {
	out, _ := m.Update(keyMsg("t"))
	if m = out.(Model); m.view != viewTheme {
		b.Fatal("t n'a pas ouvert le sélecteur de thèmes")
	}
	return m
}

// BenchmarkProgressStream : débit de la vue de progression, du canal au
// rendu, sur un pull de 10 stacks × 10 services × 30 couches qui émet
// 3 000 événements d'un coup. Mesure le coût moyen par événement.
func BenchmarkProgressStream(b *testing.B) {
	const n = 3000
	evs := make([]compose.ProgressEvent, 0, n)
	for i := range n {
		s, v, l := i%10, (i/10)%10, i%30
		name := fmt.Sprintf("stack-%02d", s)
		evs = append(evs, compose.ProgressEvent{StackName: name, Container: fmt.Sprintf("%012x", s*10000+v*100+l),
			ParentID: fmt.Sprintf("Image %s-svc%d", name, v), Status: "Downloading",
			Details: "[==>      ] 12.3MB/45.6MB", Percent: i % 100, State: "working"})
	}
	b.ReportAllocs()
	for b.Loop() {
		m := progressModel(10, 10, 30)
		ch := make(chan compose.ProgressEvent, n+1)
		for _, ev := range evs {
			ch <- ev
		}
		ch <- compose.ProgressEvent{AllDone: true}
		m.progressCh = ch
		cmd := readProgressCmd(ch, m.progressSeq)
		for !m.progressDone {
			out, next := m.Update(cmd())
			m = out.(Model)
			_ = m.View()
			cmd = next
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/n, "ns/event")
}

// Bloc de 10 000 ressources, bien plus haut que l'écran : seules les lignes
// visibles sont construites, les autres seulement comptées.
func BenchmarkProgressView10k(b *testing.B) {
	m := progressModel(10, 10, 99)
	b.ReportAllocs()
	for b.Loop() {
		_ = m.viewContent()
	}
}

// Défilement dans 10 000 lignes de logs : une frappe ↑ puis le rendu.
func BenchmarkLogsScroll(b *testing.B) {
	m := scaleModel(10)
	m.view = viewLogs
	for i := range maxLogLines {
		m.logLines = append(m.logLines, fmt.Sprintf("[web-1] ligne %d GET /api/v1/items 200", i))
	}
	m.logScroll = maxLogLines - m.logsHeight()
	up, down := keyMsg("up"), keyMsg("down")
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		k := up
		if i%2 == 1 {
			k = down
		}
		i++
		out, _ := m.Update(k)
		m = out.(Model)
		_ = m.View()
	}
}

// Recherche dans le sélecteur de thèmes : une lettre tapée après « / » sur
// tout le catalogue, puis le rendu.
func BenchmarkThemeSearchKey(b *testing.B) {
	m := openTheme(b, scaleModel(63))
	out, _ := m.Update(keyMsg("/"))
	m = out.(Model)
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		k := keyMsg("a")
		if i%2 == 1 {
			k = keyMsg("backspace")
		}
		i++
		out, _ := m.Update(k)
		m = out.(Model)
		_ = m.View()
	}
}

// Filtre du sélecteur sur tout le catalogue (659 thèmes), sans le rendu.
func BenchmarkThemeRowsFilter(b *testing.B) {
	m := openTheme(b, scaleModel(10))
	m.themeFilter = "a"
	b.ReportAllocs()
	for b.Loop() {
		_ = m.themeRows()
	}
}

// Changement de thème : palette → rôles (contraste corrigé) puis styles.
func BenchmarkApplyTheme(b *testing.B) {
	m := openTheme(b, scaleModel(10))
	m.themeFilter = "a"
	rows := m.themeRows()
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		m.useThemeRow(rows[i%len(rows)])
		i++
	}
}
