package view

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/logstream"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/gofrs/flock"
)

const testErrorQuery = "level>=error"

func testWorkbench(t *testing.T) *logWorkbench {
	t.Helper()
	w := newLogWorkbench(nil, 100)
	w.follow = false
	return w
}

func drawnText(t *testing.T, view *tview.TextView, width, height int) string {
	t.Helper()
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(width, height)
	view.SetRect(0, 0, width, height)
	view.Draw(screen)
	var out strings.Builder
	for y := range height {
		line := make([]rune, 0, width)
		for x := range width {
			r, _, _, _ := screen.GetContent(x, y)
			line = append(line, r)
		}
		out.WriteString(strings.TrimRight(string(line), " "))
		out.WriteByte('\n')
	}
	return out.String()
}
func seedWorkbench(w *logWorkbench) {
	for i, raw := range []string{`{"level":"info","message":"healthz"}`, `{"level":"error","http":{"status":500},"message":"Bearer secret [red]"}`, `level=warn msg="retry"`} {
		w.ingest([]logstream.Entry{logstream.Parse(logstream.Source{Pod: []string{"a", "b", "a"}[i], Container: "main"}, time.Now(), raw)})
	}
	w.flush()
	w.render()
}
func TestWorkbenchFilterSelectionAndSafeCopy(t *testing.T) {
	w := testWorkbench(t)
	seedWorkbench(w)
	w.selected = 2
	w.render()
	if err := w.filter("level>=error AND http.status>=500"); err != nil {
		t.Fatal(err)
	}
	w.render()
	if len(w.rows) != 1 || w.rows[0].ID != 2 {
		t.Fatalf("typed results: %+v", w.rows)
	}
	if err := w.filter("["); err == nil {
		t.Fatal("invalid filter accepted")
	}
	w.render()
	if len(w.rows) != 1 || w.selected != 2 {
		t.Fatal("invalid query changed selection/results")
	}
	w.redact = false
	if s := w.snippet(); strings.Contains(s, "Bearer secret") || !strings.Contains(s, "pod=b") {
		t.Fatalf("unsafe/missing copy provenance: %s", s)
	}
	if err := w.filter(""); err != nil {
		t.Fatal(err)
	}
	if err := w.filter("-healthz"); err != nil {
		t.Fatal(err)
	}
	w.render()
	if len(w.localRules) != 1 || len(w.rows) != 2 {
		t.Fatal("negative rule not stacked")
	}
	w.ingest([]logstream.Entry{logstream.Parse(logstream.Source{Pod: "a"}, time.Now(), "new")})
	w.flush()
	w.render()
	if w.selected != 2 {
		t.Fatal("paused selection moved")
	}
}

func TestWorkbenchFreezeKeepsDisplayedEntriesAcrossLiveEviction(t *testing.T) {
	w := newLogWorkbench(nil, 3)
	w.follow = true
	w.writer = newLogWriter(t.TempDir(), false, logstream.RecordingOptions{})
	for _, raw := range []string{"one", "two", "three"} {
		w.ingest([]logstream.Entry{logstream.Parse(logstream.Source{Pod: raw}, time.Now(), raw)})
	}
	w.flush()
	w.render()
	w.selected = 2
	w.table.Select(2, 0)
	w.table.SetOffset(1, 2)

	w.key(tcell.NewEventKey(tcell.KeyRune, 's', tcell.ModNone))
	for _, raw := range []string{"four", "five", "six"} {
		w.ingest([]logstream.Entry{logstream.Parse(logstream.Source{Pod: raw}, time.Now(), raw)})
	}
	w.flush()
	w.render()

	if got := []uint64{w.rows[0].ID, w.rows[1].ID, w.rows[2].ID}; !reflect.DeepEqual(got, []uint64{1, 2, 3}) {
		t.Fatalf("frozen IDs changed after live eviction: %v", got)
	}
	if w.selected != 2 {
		t.Fatalf("frozen selection changed: %d", w.selected)
	}
	if row, col := w.table.GetOffset(); row != 1 || col != 2 {
		t.Fatalf("frozen offset changed: (%d,%d)", row, col)
	}
	if err := w.filter("two"); err != nil {
		t.Fatal(err)
	}
	w.render()
	if len(w.rows) != 1 || w.rows[0].ID != 2 || !strings.Contains(w.snippet(), "two") {
		t.Fatalf("filter/copy left frozen snapshot: rows=%+v copy=%q", w.rows, w.snippet())
	}
	w.expand()
	w.render()
	w.key(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	if w.selected != 2 || len(w.rows) != 1 || w.rows[0].ID != 2 {
		t.Fatalf("detail return lost frozen selection: selected=%d rows=%+v", w.selected, w.rows)
	}
	if live := w.engine.Snapshot(); len(live) != 3 || live[0].ID != 4 || live[2].ID != 6 {
		t.Fatalf("capture did not continue while frozen: %+v", live)
	}
	w.writer.close()
	<-w.writer.done
	if state := w.writer.status(); state.err != "" || state.info.LastID != 6 {
		t.Fatalf("recording did not continue while frozen: %+v", state)
	}

	if err := w.filter(""); err != nil {
		t.Fatal(err)
	}
	w.key(tcell.NewEventKey(tcell.KeyRune, 's', tcell.ModNone))
	if !w.follow || w.frozenEntries != nil || w.selected != 6 {
		t.Fatalf("resume did not release snapshot and select newest: follow=%t frozen=%v selected=%d", w.follow, w.frozenEntries != nil, w.selected)
	}
}

func TestWorkbenchNavigationFreezesLastDisplayedSnapshot(t *testing.T) {
	w := newLogWorkbench(nil, 2)
	w.follow = true
	for _, raw := range []string{"one", "two"} {
		w.ingest([]logstream.Entry{logstream.Parse(logstream.Source{Pod: raw}, time.Now(), raw)})
	}
	w.flush()
	w.render()

	w.ingest([]logstream.Entry{logstream.Parse(logstream.Source{Pod: "three"}, time.Now(), "three")})
	w.flush() // The engine has evicted ID 1, but the screen has not redrawn yet.
	w.key(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone))
	w.render()

	if got := []uint64{w.rows[0].ID, w.rows[1].ID}; !reflect.DeepEqual(got, []uint64{1, 2}) {
		t.Fatalf("navigation froze a newer engine snapshot instead of the displayed rows: %v", got)
	}
}

func TestWorkbenchFrozenLiveSnapshotSurvivesHistoricalInspection(t *testing.T) {
	w := newLogWorkbench(nil, 2)
	w.follow = false // DisableAutoscroll starts frozen on the first populated draw.
	for _, raw := range []string{"live-one", "live-two"} {
		w.ingest([]logstream.Entry{logstream.Parse(logstream.Source{Pod: raw}, time.Now(), raw)})
	}
	w.flush()
	w.render()
	w.selected = 2
	w.mark = 1
	w.table.Select(2, 0)
	w.table.SetOffset(1, 2)

	w.historyPath = "recording/session"
	w.history = []logstream.Entry{{ID: 90, Raw: "history", Message: "history"}}
	w.mode = modeHistory
	w.render()
	if len(w.rows) != 1 || w.rows[0].ID != 90 {
		t.Fatalf("history did not render independently: %+v", w.rows)
	}
	w.selected = 90
	w.mark = 90
	w.table.SetOffset(0, 0)
	w.key(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	if got := []uint64{w.rows[0].ID, w.rows[1].ID}; !reflect.DeepEqual(got, []uint64{1, 2}) {
		t.Fatalf("history replaced frozen live state: ids=%v", got)
	}
	if row, col := w.table.GetOffset(); w.selected != 2 || w.mark != 1 || row != 1 || col != 2 {
		t.Fatalf("history replaced frozen selection/viewport: selected=%d mark=%d offset=(%d,%d)", w.selected, w.mark, row, col)
	}

	w.stop()
	if w.frozenSet || w.frozenEntries != nil || w.renderedEntries != nil {
		t.Fatal("stop retained a cross-session display snapshot")
	}
}
func TestWorkbenchModesAndMarkup(t *testing.T) {
	w := testWorkbench(t)
	seedWorkbench(w)
	for key, mode := range map[rune]string{'p': "patterns", 'v': "sources", 'h': "timeline", '?': "help", 'n': "rules", 'o': "raw"} {
		w.mode = modeEntries
		w.key(tcell.NewEventKey(tcell.KeyRune, key, tcell.ModNone))
		if w.mode != mode {
			t.Fatalf("%c -> %s", key, w.mode)
		}
	}
	w.mode = modeEntries
	w.selected = 2
	w.expand()
	if w.mode != "detail" || strings.Contains(w.detail.GetText(false), "Bearer secret") || !strings.Contains(w.detail.GetText(false), "[red[]") {
		t.Fatal("detail unsafe or unreachable", w.detail.GetText(false))
	}
	w.mode = modeEntries
	w.selected = 1
	w.mark = 999
	w.render()
	if !strings.Contains(w.snippet(), "evicted") {
		t.Fatal("missing evicted boundary notice")
	}
}

func TestWorkbenchDetailSectionsPreserveOriginalTextAndEscapeMarkup(t *testing.T) {
	w := testWorkbench(t)
	styles := config.NewStyles()
	styles.K9s.Views.Log.FgColor = config.NewColor("green")
	w.owner = &Log{app: &App{App: &ui.App{
		Application: tview.NewApplication(), Configurator: ui.Configurator{Styles: styles},
	}}}
	e := logstream.Entry{
		ID: 7, LastID: 8, Occurrences: 2, Repeats: 2, Format: "json", Level: "warn",
		RuntimeTime:     time.Date(2026, 9, 7, 1, 2, 3, 0, time.UTC),
		LastRuntimeTime: time.Date(2026, 9, 7, 1, 2, 4, 0, time.UTC),
		Source:          logstream.Source{Cluster: "[red]prod", Namespace: "ns", Pod: "pod", Container: "app"},
		OriginalLines:   []string{"first [red]line", "second\x1b[31m line"},
		Fields: map[string]any{
			"message": "[blue]not markup", "status": json.Number("503"),
			"http": map[string]any{"headers": []any{"one", "two"}},
		},
	}
	w.rows, w.selected = []logstream.Entry{e}, 7
	w.expand()

	plain := drawnText(t, w.detail, 160, 30)
	logAt := strings.Index(plain, "Original log")
	fieldsAt := strings.Index(plain, "Parsed fields\n")
	metadataAt := strings.Index(plain, "k9+ metadata\n")
	controlsAt := strings.Index(plain, "Controls\n")
	if logAt < 0 || fieldsAt <= logAt || metadataAt <= fieldsAt || controlsAt <= metadataAt {
		t.Fatalf("detail section order or preserved multiline text is wrong:\n%s", plain)
	}
	if !strings.Contains(plain, "first [red]line\nsecond line") ||
		!strings.Contains(plain, "\"message\": \"[blue]not markup\"") || !strings.Contains(plain, "\"status\": 503") ||
		!strings.Contains(plain, "\"http\": {\n    \"headers\": [\n      \"one\",\n      \"two\"") {
		t.Fatalf("parsed fields missing literal values:\n%s", plain)
	}
	markup := w.detail.GetText(false)
	if strings.Contains(markup, "first [red]line") || strings.Contains(markup, "[blue]not markup") || strings.Contains(markup, "[red]prod") {
		t.Fatalf("log-controlled tview tags were not escaped: %q", markup)
	}
	if !strings.Contains(markup, "[#008000::]first [red[]line") {
		t.Fatalf("original log does not use the active theme foreground: %q", markup)
	}
}

func TestWorkbenchDetailUsesSafeRawFallback(t *testing.T) {
	w := testWorkbench(t)
	e := logstream.Entry{
		ID: 1, LastID: 1, Occurrences: 1, Repeats: 1, Format: "raw", Level: "error",
		RuntimeTime: time.Now(), LastRuntimeTime: time.Now(), Raw: "Bearer secret-token [red]boom",
	}
	w.rows, w.selected = []logstream.Entry{e}, 1
	w.expand()

	plain := drawnText(t, w.detail, 160, 20)
	if !strings.Contains(plain, "Original log") || !strings.Contains(plain, "\nBearer [REDACTED] [red]boom\n") || strings.Contains(plain, "secret-token") {
		t.Fatalf("raw fallback was not safely rendered: %q", plain)
	}
	if strings.Contains(w.detail.GetText(false), "[red]boom") {
		t.Fatalf("raw fallback injected formatting: %q", w.detail.GetText(false))
	}
}

func TestWorkbenchDetailSeverityUsesThemeStatusColors(t *testing.T) {
	for _, tc := range []struct {
		level, color string
	}{
		{level: "warn", color: "#ffff00"},
		{level: "error", color: "#ff0000"},
	} {
		t.Run(tc.level, func(t *testing.T) {
			w := testWorkbench(t)
			styles := config.NewStyles()
			styles.K9s.Frame.Status.PendingColor = config.NewColor("yellow")
			styles.K9s.Frame.Status.ErrorColor = config.NewColor("red")
			w.owner = &Log{app: &App{App: &ui.App{
				Application: tview.NewApplication(), Configurator: ui.Configurator{Styles: styles},
			}}}
			e := logstream.Parse(logstream.Source{}, time.Now(), `level=`+tc.level+` msg="failure"`)
			e.ID, e.LastID = 1, 1
			w.rows, w.selected = []logstream.Entry{e}, 1
			w.expand()
			if markup := w.detail.GetText(false); !strings.Contains(markup, "["+tc.color+"::b]"+strings.ToUpper(tc.level)) {
				t.Fatalf("severity badge does not use theme status color: %q", markup)
			}
		})
	}
}

func TestWorkbenchDetailRendersMarkerAsSingleNoticeOutsideLogSections(t *testing.T) {
	w := testWorkbench(t)
	now := time.Date(2026, 9, 7, 2, 3, 4, 0, time.UTC)
	message := "Bearer secret-token [red]pod restarted"
	e := logstream.Entry{
		ID: 9, LastID: 9, Occurrences: 1, Repeats: 1, Format: "marker",
		RuntimeTime: now, LastRuntimeTime: now, Raw: message, OriginalLines: []string{message},
		Marker: &logstream.Marker{Kind: "pod-status", Origin: "client", Time: now, Approximate: true, Message: message},
	}
	w.rows, w.selected = []logstream.Entry{e}, 9
	w.expand()

	plain := drawnText(t, w.detail, 160, 24)
	if strings.Contains(plain, "Original log") || strings.Contains(plain, "Parsed fields") {
		t.Fatalf("marker was mislabeled as an application log:\n%s", plain)
	}
	if !strings.Contains(plain, "k9+ Notice\norigin client | kind pod-status | time 2026-09-07T02:03:04Z | approximate true") ||
		strings.Count(plain, "Bearer [REDACTED] [red]pod restarted") != 1 {
		t.Fatalf("marker notice missing, unsafe, or duplicated:\n%s", plain)
	}
	if !strings.Contains(plain, "k9+ metadata\n") || !strings.Contains(plain, "Controls\n") {
		t.Fatalf("marker detail lost metadata or controls:\n%s", plain)
	}
	if strings.Contains(w.detail.GetText(false), "[red]pod restarted") {
		t.Fatalf("marker notice injected formatting: %q", w.detail.GetText(false))
	}
}

func TestWorkbenchDiskReadOnlyAndSafeExport(t *testing.T) {
	dir := t.TempDir()
	r, err := logstream.OpenRecorder(dir, logstream.RecordingOptions{Raw: true})
	if err != nil {
		t.Fatal(err)
	}
	e := logstream.Parse(logstream.Source{Pod: "a"}, time.Now(), "Bearer secret")
	e.ID = 1
	if err = r.Append(e); err != nil {
		t.Fatal(err)
	}
	r.Close()
	before, _ := os.ReadFile(filepath.Join(dir, "session.json"))
	entries, err := scanLogHistory(context.Background(), dir, 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "session.json"))
	if !bytes.Equal(before, after) || len(entries) != 1 {
		t.Fatal("history modified session")
	}
	var out strings.Builder
	if _, err = scanLogHistory(context.Background(), dir, 0, nil, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Bearer secret") {
		t.Fatal("raw history unsafe export")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = scanLogHistory(ctx, dir, 0, nil, nil); err == nil {
		t.Fatal("scan ignored cancellation")
	}
}

func TestWorkbenchTornTailRecoversPagingSearchAndExport(t *testing.T) {
	previous := config.AppConfigDir
	config.AppConfigDir = t.TempDir()
	defer func() { config.AppConfigDir = previous }()
	dir := t.TempDir()
	var data []byte
	for i, raw := range []string{"keep first", "find second"} {
		e := logstream.Parse(logstream.Source{Pod: "p"}, time.Now(), raw)
		e.ID = uint64(i + 1)
		encoded, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, encoded...)
		data = append(data, '\n')
	}
	data = append(data, `{"ID":3,"Raw":"torn`...)
	if err := os.WriteFile(filepath.Join(dir, "00000000000000000001.jsonl"), data, 0600); err != nil {
		t.Fatal(err)
	}

	w := testWorkbench(t)
	w.historyPath = dir
	waitIO := func() {
		deadline := time.Now().Add(2 * time.Second)
		for w.ioRunning && time.Now().Before(deadline) {
			w.consumeIO()
			time.Sleep(time.Millisecond)
		}
		w.consumeIO()
	}
	w.readHistory(false)
	waitIO()
	if len(w.history) != 2 || !strings.Contains(strings.ToLower(w.notice), "torn") {
		t.Fatal("paging discarded valid records or hid torn-tail uncertainty", w.history, w.notice)
	}
	if err := w.filter("find"); err != nil {
		t.Fatal(err)
	}
	w.historyBefore = 0
	w.readHistory(true)
	waitIO()
	if len(w.history) != 1 || w.history[0].Raw != "find second" || !strings.Contains(strings.ToLower(w.notice), "torn") {
		t.Fatal("search discarded valid match or hid torn-tail uncertainty", w.history, w.notice)
	}
	w.exportDisk()
	waitIO()
	path := strings.TrimPrefix(w.notice, "Safe full disk export with torn-tail recovery uncertainty: ")
	exported, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(w.notice, err)
	}
	if !strings.Contains(string(exported), "keep first") || !strings.Contains(string(exported), "find second") || !strings.Contains(strings.ToLower(string(exported)), "torn") {
		t.Fatal("export omitted valid records or recovery metadata", string(exported))
	}
}

func TestWorkbenchMalformedCompleteHistoryLineRemainsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00000000000000000001.jsonl"), []byte("{malformed}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := scanLogHistory(context.Background(), dir, 0, nil, nil); err == nil {
		t.Fatal("newline-terminated malformed record was ignored")
	}
}

func TestWorkbenchOversizedTornTailKeepsCompletePrefix(t *testing.T) {
	dir := t.TempDir()
	entry := logstream.Parse(logstream.Source{Pod: "p"}, time.Now(), "complete")
	entry.ID = 1
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	data = append(data, strings.Repeat("x", 2<<20)...)
	if err = os.WriteFile(filepath.Join(dir, "00000000000000000001.jsonl"), data, 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := scanLogHistory(context.Background(), dir, 0, nil, nil)
	if !errors.Is(err, errLogHistoryTornTail) || len(entries) != 1 || entries[0].Raw != "complete" {
		t.Fatal("oversized torn tail discarded complete bounded prefix", entries, err)
	}
}
func TestWorkbenchWriterDrainsAndReportsErrors(t *testing.T) {
	dir := t.TempDir()
	wr := newLogWriter(dir, false, logstream.RecordingOptions{})
	var entries []logstream.Entry
	for i := 1; i <= 300; i++ {
		e := logstream.Parse(logstream.Source{}, time.Now(), "hello")
		e.ID = uint64(i)
		entries = append(entries, e)
	}
	wr.offer(entries)
	wr.close()
	select {
	case <-wr.done:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not drain")
	}
	state := wr.status()
	if state.err != "" || state.info.LastID != 300 {
		t.Fatal(state)
	}
	bad := newLogWriter(filepath.Join(dir, "session.json", "bad"), false, logstream.RecordingOptions{})
	bad.close()
	<-bad.done
	if bad.status().err == "" {
		t.Fatal("recording error invisible")
	}
}

func TestWorkbenchPromptCommitAndCancel(t *testing.T) {
	l := NewLog(client.PodGVR, &dao.LogOptions{Path: "ns/p"})
	if err := l.Init(makeContext(t)); err != nil {
		t.Fatal(err)
	}
	l.logs.cmdBuff.AddListener(l)
	defer l.logs.cmdBuff.RemoveListener(l)
	seedWorkbench(l.workbench)
	l.logs.cmdBuff.SetActive(true)
	l.logs.cmdBuff.SetText(testErrorQuery, "", true)
	if l.workbench.expression != "" {
		t.Fatal("applied before prompt commit")
	}
	l.logs.cmdBuff.SetActive(false)
	if l.workbench.expression != testErrorQuery || l.workbench.mode == "detail" {
		t.Fatal("Enter conflicted with detail")
	}
	l.logs.cmdBuff.SetActive(true)
	l.logs.cmdBuff.SetText("[", "", true)
	l.logs.cmdBuff.SetActive(false)
	if l.workbench.expression != testErrorQuery || !strings.Contains(l.workbench.notice, "ERROR") {
		t.Fatal("invalid prompt lost prior query")
	}
	l.logs.cmdBuff.SetActive(true)
	l.logs.cmdBuff.SetText("-healthz", "", true)
	l.logs.cmdBuff.SetActive(false)
	if len(l.workbench.localRules) != 1 {
		t.Fatal("exclusion not committed once")
	}
}

func TestWorkbenchMarkersLanesAndHistogramSelection(t *testing.T) {
	w := testWorkbench(t)
	seedWorkbench(w)
	w.resume()
	marker := logstream.Entry{RuntimeTime: time.Now(), Raw: "forbidden", Marker: &logstream.Marker{Kind: "events-unavailable", Origin: "Kubernetes", Message: "forbidden", Approximate: true}}
	w.ingest([]logstream.Entry{marker})
	w.flush()
	if err := w.filter(testErrorQuery); err != nil {
		t.Fatal(err)
	}
	w.render()
	if len(w.rows) != 2 || w.rows[1].Marker == nil {
		t.Fatal("severity filtered marker")
	}
	if err := w.filter(""); err != nil {
		t.Fatal(err)
	}
	w.showSources()
	w.chooseLane(1)
	w.chooseLane(2)
	if w.mode != "lanes" || w.laneA == w.laneB || w.table.GetColumnCount() != 2 {
		t.Fatal("two distinct lanes unavailable")
	}
	w.render()
	if w.mode != "lanes" {
		t.Fatal("refresh left lanes")
	}
	w.showTimeline()
	row, _ := w.table.GetSelection()
	w.render()
	after, _ := w.table.GetSelection()
	if row != after {
		t.Fatal("timeline selection moved")
	}
}

func TestOwnedRecordingSessionCap(t *testing.T) {
	root := t.TempDir()
	foreign := filepath.Join(root, "session-foreign")
	if err := os.Mkdir(foreign, 0700); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if _, err := newLogSession(root, 2, 24); err != nil {
			t.Fatal(err)
		}
	}
	sessions, err := listLogSessions(root)
	if err != nil || len(sessions) != 2 {
		t.Fatal("session cap", sessions, err)
	}
	if _, err = os.Stat(foreign); err != nil {
		t.Fatal("deleted unowned path")
	}
}

func TestWorkbenchHistogramUsesSourceFilterAndHistoryScope(t *testing.T) {
	w := testWorkbench(t)
	seedWorkbench(w)
	w.selected = 2
	w.key(tcell.NewEventKey(tcell.KeyRune, 'i', tcell.ModNone))
	w.render()
	var visible uint64
	for _, b := range w.buckets {
		visible += b.Visible
	}
	if visible != 1 {
		t.Fatalf("source-filtered histogram visible=%d", visible)
	}
	w.isolated = ""
	w.mark = 2
	w.historyPath = "another-session"
	w.history = []logstream.Entry{w.rows[0]}
	w.mode = modeHistory
	w.render()
	if w.mark != 0 {
		t.Fatal("live mark silently reused historical ID")
	}
	if !strings.Contains(w.status.GetText(true), "visible:1/1") {
		t.Fatal("history status used live counts", w.status.GetText(true))
	}
}

func TestWorkbenchStatusShowsToggleHotkeys(t *testing.T) {
	w := testWorkbench(t)
	w.follow = true
	w.render()
	status := w.status.GetText(true)
	for _, want := range []string{"follow:true(s)", "safe:true(d)", "collapse:true(b)", "group:true(u)"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status does not expose toggle hotkey %q: %s", want, status)
		}
	}
	w.follow, w.redact, w.collapse, w.multiline = false, false, false, false
	w.render()
	status = w.status.GetText(true)
	for _, want := range []string{"follow:false(s)", "safe:false(d)", "collapse:false(b)", "group:false(u)"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status lost toggle hotkey %q after toggle: %s", want, status)
		}
	}
}

func TestWorkbenchFullWidthDetailDrawAndRecordError(t *testing.T) {
	w := testWorkbench(t)
	seedWorkbench(w)
	w.SetRect(0, 0, 120, 30)
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(120, 30)
	w.Draw(screen)
	w.selected = 2
	w.expand()
	w.Draw(screen)
	_, _, width, _ := w.detail.GetInnerRect()
	if width < 115 {
		t.Fatalf("detail width %d", width)
	}
	w.writer = newLogWriter(filepath.Join(t.TempDir(), "missing-parent", "safe"), false, logstream.RecordingOptions{MaxBytes: 1})
	w.writer.close()
	<-w.writer.done
	w.render()
	if !strings.Contains(w.status.GetText(true), "RECORD ERROR:") {
		t.Fatal("recording failure not rendered")
	}
}

func TestWorkbenchClosedRecorderDoesNotReportIntentionalLoss(t *testing.T) {
	wr := newLogWriter(t.TempDir(), false, logstream.RecordingOptions{})
	wr.close()
	<-wr.done
	wr.offer([]logstream.Entry{{ID: 1, Raw: "after explicit stop"}})
	if wr.status().dropped != 0 {
		t.Fatal("explicit stop counted as lost recording", wr.status())
	}
}

func TestWorkbenchSparklineRenderedMarkupAndSourceWidth(t *testing.T) {
	w := testWorkbench(t)
	seedWorkbench(w)
	w.SetRect(0, 0, 120, 30)
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(120, 30)
	w.Draw(screen)
	var line strings.Builder
	for x := range 120 {
		r, _, _, _ := screen.GetContent(x, 1)
		line.WriteRune(r)
	}
	if strings.Contains(line.String(), "[gray]") || !strings.Contains(line.String(), "record off") {
		t.Fatal("sparkline markup obscures status", line.String())
	}
	if got := sourceName(logstream.Source{Pod: "mixed-logs-977466bf9-dlgvh", Container: "sidecar"}); got != "dlgvh/sidecar#0" {
		t.Fatal("source gutter not compact", got)
	}
}

func TestWorkbenchPatternsPreserveSensitiveProvenance(t *testing.T) {
	w := testWorkbench(t)
	e := logstream.Parse(logstream.Source{Pod: "p"}, time.Now(), "PRIVATECONTINUATION")
	e.Sensitive = true
	w.ingest([]logstream.Entry{e})
	w.flush()
	w.render()
	w.showPatterns()
	if strings.Contains(w.table.GetCell(1, 2).Text, "PRIVATECONTINUATION") {
		t.Fatal("pattern exposed private-key continuation")
	}
	w.table.Select(1, 0)
	w.expand()
	if w.mode != "pattern" || len(w.rows) != 1 {
		t.Fatal("safe pattern drilldown lost original identity")
	}
}

func TestWorkbenchJSONPatternDrilldownAndRedactionToggle(t *testing.T) {
	w := testWorkbench(t)
	for i := range 3 {
		raw := fmt.Sprintf(`{"level":"error","message":"Bearer secret","http":{"status":500},"attempt":%d}`, i)
		w.ingest([]logstream.Entry{logstream.Parse(logstream.Source{Pod: fmt.Sprint(i)}, time.Now(), raw)})
	}
	w.flush()
	w.render()
	w.redact = false
	w.showPatterns()
	w.key(tcell.NewEventKey(tcell.KeyRune, 'd', tcell.ModNone))
	if strings.Contains(w.table.GetCell(1, 2).Text, "Bearer secret") {
		t.Fatal("enabling redaction left unsafe frozen pattern")
	}
	w.table.Select(1, 0)
	w.expand()
	if len(w.rows) != 3 {
		t.Fatalf("pattern drilldown=%d rows", len(w.rows))
	}
}

func TestWorkbenchRealPromptNegativeSubmission(t *testing.T) {
	l := NewLog(client.PodGVR, &dao.LogOptions{Path: "ns/p"})
	if err := l.Init(makeContext(t)); err != nil {
		t.Fatal(err)
	}
	l.app.App.Init()
	l.app.bindKeys()
	l.logs.cmdBuff.AddListener(l)
	defer l.logs.cmdBuff.RemoveListener(l)
	send := func(evt *tcell.EventKey) {
		if evt = l.app.keyboard(evt); evt != nil {
			l.app.Prompt().SendKey(evt)
		}
	}
	submit := func(text string) {
		l.app.ResetPrompt(l.logs.cmdBuff)
		send(tcell.NewEventKey(tcell.KeyCtrlU, 0, tcell.ModNone))
		for _, r := range text {
			send(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
		}
		send(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	}
	submit(testErrorQuery)
	submit("[")
	submit("-cache miss")
	if l.workbench.expression != testErrorQuery || len(l.workbench.localRules) != 1 || l.workbench.localRules[0] != "cache miss" {
		t.Fatalf("real prompt query=%q rules=%v", l.workbench.expression, l.workbench.localRules)
	}
}

func TestWorkbenchRawRecordingOverridesGlobalRedraw(t *testing.T) {
	previous := config.AppConfigDir
	config.AppConfigDir = t.TempDir()
	defer func() { config.AppConfigDir = previous }()
	l := NewLog(client.PodGVR, &dao.LogOptions{Path: "ns/p"})
	if err := l.Init(makeContext(t)); err != nil {
		t.Fatal(err)
	}
	globalCalled := false
	l.originalCapture = func(evt *tcell.EventKey) *tcell.EventKey { globalCalled = true; return evt }
	l.captureWorkbenchKey(tcell.NewEventKey(tcell.KeyCtrlR, 0, tcell.ModNone))
	deadline := time.Now().Add(2 * time.Second)
	for l.workbench.recordStart != nil && time.Now().Before(deadline) {
		l.workbench.consumeRecordingStart()
		time.Sleep(time.Millisecond)
	}
	if globalCalled || !l.workbench.writerState().info.Raw {
		t.Fatal("raw recording intercepted by redraw")
	}
	l.workbench.stop()
	<-l.workbench.writer.done
}

func TestLogLifecycleKeepsOnlyLatestQueuedRequest(t *testing.T) {
	l := NewLog(client.PodGVR, &dao.LogOptions{})
	entered, release, latest := make(chan context.Context, 1), make(chan struct{}), make(chan struct{})
	l.runModel(func(ctx context.Context) { entered <- ctx; <-release })
	ctx := <-entered
	obsolete := false
	l.runModel(func(context.Context) { obsolete = true })
	l.runModel(func(context.Context) { close(latest) })
	select {
	case <-ctx.Done():
	default:
		t.Fatal("previous lifecycle not canceled")
	}
	close(release)
	select {
	case <-latest:
	case <-time.After(time.Second):
		t.Fatal("latest lifecycle not run")
	}
	if obsolete {
		t.Fatal("obsolete lifecycle resurrected collector")
	}
	l.cancel()
}

func TestWorkbenchVisibleExportBackgroundSafe(t *testing.T) {
	l := NewLog(client.PodGVR, &dao.LogOptions{Path: "ns/p"})
	if err := l.Init(makeContext(t)); err != nil {
		t.Fatal(err)
	}
	l.app.Config.K9s.ScreenDumpDir = t.TempDir()
	seedWorkbench(l.workbench)
	l.workbench.redact = false
	l.workbench.exportVisible()
	if !l.workbench.ioRunning {
		t.Fatal("visible export executes on draw thread")
	}
	deadline := time.Now().Add(2 * time.Second)
	for l.workbench.ioRunning && time.Now().Before(deadline) {
		l.workbench.consumeIO()
		time.Sleep(time.Millisecond)
	}
	if l.workbench.ioRunning || !strings.HasPrefix(l.workbench.notice, "Safe visible export: ") {
		t.Fatal(l.workbench.notice)
	}
	data, err := os.ReadFile(strings.TrimPrefix(l.workbench.notice, "Safe visible export: "))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "Bearer secret") || !strings.Contains(string(data), "pod=b") {
		t.Fatal("unsafe or incomplete visible export")
	}
}

func TestWorkbenchHistoricalTimelineRetainsScopeAcrossRefreshAndJump(t *testing.T) {
	w := testWorkbench(t)
	seedWorkbench(w)
	historical := logstream.Parse(logstream.Source{Pod: "old-session"}, time.Now().Add(-24*time.Hour), "historical spike")
	historical.ID = 2
	historical.Occurrences = 9
	w.historyPath = "old-session-dir"
	w.history = []logstream.Entry{historical}
	w.mode = modeHistory
	w.render()
	w.key(tcell.NewEventKey(tcell.KeyRune, 'h', tcell.ModNone))
	w.render()
	if !strings.Contains(w.status.GetText(true), "retained disk") {
		t.Fatal("timeline silently changed scope", w.status.GetText(true))
	}
	var found int = -1
	for i, b := range w.buckets {
		if b.EntryID == 2 && b.Observed == 9 {
			found = i
		}
	}
	if found < 0 {
		t.Fatal("historical buckets replaced on periodic refresh")
	}
	w.table.Select(found+1, 0)
	w.key(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if w.mode != modeHistory || w.selectionScope != "old-session-dir" || w.selected != 2 || w.rows[0].Source.Pod != "old-session" {
		t.Fatal("historical Enter jumped to live identity")
	}
	w.key(tcell.NewEventKey(tcell.KeyRune, 'J', tcell.ModNone))
	if w.mode != modeHistory || w.rows[0].Raw != "historical spike" {
		t.Fatal("history J jumped to live")
	}
	w.key(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	w.key(tcell.NewEventKey(tcell.KeyRune, 'm', tcell.ModNone))
	w.key(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	if w.mode != modeHistory || w.mark != 2 {
		t.Fatal("detail return lost session or explicit mark")
	}
}

func TestWorkbenchSecondaryEntryActionsRejectStaleRows(t *testing.T) {
	w := testWorkbench(t)
	seedWorkbench(w)
	w.selected = 2
	for _, mode := range []string{"patterns", "sources", "lanes", "timeline", "sessions", "rules", "help"} {
		for _, evt := range []*tcell.EventKey{tcell.NewEventKey(tcell.KeyRune, 'c', tcell.ModNone), tcell.NewEventKey(tcell.KeyRune, 'm', tcell.ModNone), tcell.NewEventKey(tcell.KeyCtrlS, 0, tcell.ModNone)} {
			w.mode = mode
			w.mark = 1
			w.notice = ""
			w.key(evt)
			if !strings.Contains(w.notice, "entry action unavailable") || w.mark != 1 || w.ioRunning {
				t.Fatalf("%s silently operated on old entries: %q", mode, w.notice)
			}
		}
	}
	for _, mode := range []string{"lanes", "rules", "help"} {
		w.mode = mode
		w.expand()
		if w.mode != mode || !strings.Contains(w.notice, "entry action unavailable") {
			t.Fatalf("%s Enter expanded stale entry", mode)
		}
	}
}

func TestWorkbenchRecordingStartBackgroundCancellation(t *testing.T) {
	previous := config.AppConfigDir
	config.AppConfigDir = t.TempDir()
	defer func() { config.AppConfigDir = previous }()
	w := testWorkbench(t)
	if err := os.MkdirAll(w.recordingRoot(), 0700); err != nil {
		t.Fatal(err)
	}
	rootLock := flock.New(filepath.Join(w.recordingRoot(), logRootLockName))
	if err := rootLock.Lock(); err != nil {
		t.Fatal(err)
	}
	held := true
	defer func() {
		if held {
			_ = rootLock.Close()
		}
	}()
	w.key(tcell.NewEventKey(tcell.KeyRune, 'R', tcell.ModNone))
	op := w.recordStart
	if op == nil || w.writerState().path != "" {
		t.Fatal("recording preparation did not stay background")
	}
	w.render()
	if !strings.Contains(w.status.GetText(true), "record starting") {
		t.Fatal("missing recording-start status")
	}
	w.key(tcell.NewEventKey(tcell.KeyRune, 'R', tcell.ModNone))
	select {
	case <-op.done:
	case <-time.After(time.Second):
		t.Fatal("R could not cancel waiting maintenance")
	}
	w.consumeRecordingStart()
	if w.writer != nil {
		t.Fatal("canceled R started writer")
	}
	w.key(tcell.NewEventKey(tcell.KeyCtrlR, 0, tcell.ModNone))
	op = w.recordStart
	w.stop()
	select {
	case <-op.done:
	case <-time.After(time.Second):
		t.Fatal("Stop blocked on recording maintenance")
	}
	if err := rootLock.Close(); err != nil {
		t.Fatal(err)
	}
	held = false
	w.consumeRecordingStart()
	if w.writer != nil {
		t.Fatal("Stop allowed stale raw recording")
	}
	sessions, err := listLogSessions(w.recordingRoot())
	if err != nil || len(sessions) != 0 {
		t.Fatal("canceled starts left sessions", sessions, err)
	}
}

func TestWorkbenchStopRejectsPreparedRecording(t *testing.T) {
	previous := config.AppConfigDir
	config.AppConfigDir = t.TempDir()
	defer func() { config.AppConfigDir = previous }()
	w := testWorkbench(t)
	w.startRecording(false)
	op := w.recordStart
	deadline := time.Now().Add(time.Second)
	for len(op.result) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(op.result) == 0 {
		t.Fatal("recording was not prepared")
	}
	if _, err := prepareLogSession(context.Background(), w.recordingRoot(), 1, 24); err == nil {
		t.Fatal("prepared active session was eligible for pruning")
	}
	w.stop()
	<-op.done
	w.consumeRecordingStart()
	if w.writer != nil {
		t.Fatal("prepared result resurrected writer after Stop")
	}
	sessions, err := listLogSessions(w.recordingRoot())
	if err != nil || len(sessions) != 0 {
		t.Fatal("unclaimed prepared session leaked", sessions, err)
	}
}

func TestPrepareLogSessionReclaimsStaleActiveMarker(t *testing.T) {
	root := t.TempDir()
	stale, err := newLogSession(root, 8, 24)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(stale, "active"), []byte("active\n"), 0600); err != nil {
		t.Fatal(err)
	}

	path, lease, err := prepareReservedLogSession(context.Background(), root, 1, 24)
	if err != nil {
		t.Fatal("stale active marker consumed the only session slot", err)
	}
	defer func() {
		_ = lease.Close()
		_ = removeLogSession(context.Background(), path)
	}()
	if _, err = os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale owned session was not reclaimed", err)
	}
}

func TestPrepareLogSessionProtectsLiveLease(t *testing.T) {
	root := t.TempDir()
	path, lease, err := prepareReservedLogSession(context.Background(), root, 1, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = lease.Close()
		_ = removeLogSession(context.Background(), path)
	}()
	if _, _, err = prepareReservedLogSession(context.Background(), root, 1, 24); err == nil {
		t.Fatal("live leased session was pruned or failed to consume its slot")
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("live leased session was removed", err)
	}
}

func TestPrepareLogSessionWaitsForCrossProcessRootLease(t *testing.T) {
	root := t.TempDir()
	rootLock := flock.New(filepath.Join(root, ".k9plus-recordings.lock"))
	if err := rootLock.Lock(); err != nil {
		t.Fatal(err)
	}
	result := filepath.Join(root, "child-result")
	start := filepath.Join(root, "start")
	release := filepath.Join(root, "release")
	cmd := logSessionHelperCommand(root, result, start, release)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	awaitLogSessionHelperResult(t, result+".ready")
	if err := os.WriteFile(start, []byte("start"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(release, []byte("release"), 0600)
		_ = rootLock.Unlock()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})
	time.Sleep(150 * time.Millisecond)
	if data, err := os.ReadFile(result); err == nil {
		t.Fatalf("session preparation ignored cross-process root lease: %s", data)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := rootLock.Unlock(); err != nil {
		t.Fatal(err)
	}
	if data := awaitLogSessionHelperResult(t, result); !strings.HasPrefix(data, "ok:") {
		t.Fatalf("session preparation did not resume after root lease release: %s", data)
	}
	if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	cmd.Process = nil
}

func TestConcurrentProcessesPublishOnlyOneLeasedSessionAtCap(t *testing.T) {
	root := t.TempDir()
	start := filepath.Join(root, "start")
	release := filepath.Join(root, "release")
	results := []string{filepath.Join(root, "result-1"), filepath.Join(root, "result-2")}
	commands := []*exec.Cmd{
		logSessionHelperCommand(root, results[0], start, release),
		logSessionHelperCommand(root, results[1], start, release),
	}
	for _, cmd := range commands {
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
	}
	for _, result := range results {
		awaitLogSessionHelperResult(t, result+".ready")
	}
	t.Cleanup(func() {
		_ = os.WriteFile(release, []byte("release"), 0600)
		for _, cmd := range commands {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			_ = cmd.Wait()
		}
	})
	if err := os.WriteFile(start, []byte("start"), 0600); err != nil {
		t.Fatal(err)
	}
	first := awaitLogSessionHelperResult(t, results[0])
	second := awaitLogSessionHelperResult(t, results[1])
	oks := 0
	for _, result := range []string{first, second} {
		if strings.HasPrefix(result, "ok:") {
			oks++
		} else if !strings.HasPrefix(result, "err:") {
			t.Fatalf("unexpected helper result: %s", result)
		}
	}
	if oks != 1 {
		t.Fatalf("published sessions without exclusive live ownership: %q, %q", first, second)
	}
	if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
		cmd.Process = nil
	}
}

func TestPrepareLogSessionRejectsLockSymlinks(t *testing.T) {
	for _, name := range []string{"root", "active"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			outside := filepath.Join(t.TempDir(), "must-not-be-created")
			if name == "root" {
				if err := os.Symlink(outside, filepath.Join(root, ".k9plus-recordings.lock")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			} else {
				session := filepath.Join(root, "session-owned")
				if err := os.Mkdir(session, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(session, "k9plus-owned"), []byte("log-workbench-v1\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(session, "active")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			if _, err := prepareLogSession(context.Background(), root, 1, 24); err == nil {
				t.Fatal("lock symlink was accepted")
			}
			if _, err := os.Lstat(outside); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("lock symlink created or changed a file outside the recording root", err)
			}
		})
	}
}

func TestLogSessionProcessHelper(t *testing.T) {
	root := os.Getenv("K9S_TEST_LOG_SESSION_ROOT")
	if root == "" {
		return
	}
	if err := os.WriteFile(os.Getenv("K9S_TEST_LOG_SESSION_RESULT")+".ready", []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	start := os.Getenv("K9S_TEST_LOG_SESSION_START")
	for {
		if _, err := os.Stat(start); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	path, lease, err := prepareReservedLogSession(context.Background(), root, 1, 24)
	result := "err:"
	if err == nil {
		result = "ok:" + path
	} else {
		result += err.Error()
	}
	if writeErr := os.WriteFile(os.Getenv("K9S_TEST_LOG_SESSION_RESULT"), []byte(result), 0600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if lease == nil {
		return
	}
	release := os.Getenv("K9S_TEST_LOG_SESSION_RELEASE")
	for {
		if _, statErr := os.Stat(release); statErr == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func logSessionHelperCommand(root, result, start, release string) *exec.Cmd {
	cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestLogSessionProcessHelper$")
	cmd.Env = append(os.Environ(),
		"K9S_TEST_LOG_SESSION_ROOT="+root,
		"K9S_TEST_LOG_SESSION_RESULT="+result,
		"K9S_TEST_LOG_SESSION_START="+start,
		"K9S_TEST_LOG_SESSION_RELEASE="+release,
	)
	return cmd
}

func awaitLogSessionHelperResult(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			return string(data)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for subprocess result", path)
	return ""
}

func TestAppRecordingShutdownFlushesDrainsAndReleasesSession(t *testing.T) {
	root := t.TempDir()
	path, lease, err := prepareReservedLogSession(context.Background(), root, 2, 24)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{}
	w := testWorkbench(t)
	w.writer = newLogWriter(path, false, logstream.RecordingOptions{}, lease)
	app.registerLogWorkbench(w)
	app.registerLogWriter(w.writer)
	w.ingest([]logstream.Entry{logstream.Parse(logstream.Source{Pod: "p"}, time.Now(), "pending at exit")})

	app.shutdownLogRecordings()

	entries, err := scanLogHistory(context.Background(), path, 0, nil, nil)
	if err != nil || len(entries) != 1 || entries[0].Raw != "pending at exit" {
		t.Fatal("normal shutdown did not flush and drain recording", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(path, "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		WriterOpen bool
	}
	if err = json.Unmarshal(data, &meta); err != nil || meta.WriterOpen {
		t.Fatal("normal shutdown left writer-open checkpoint", string(data), err)
	}
	if _, err = os.Stat(filepath.Join(path, "active")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("normal shutdown left activity marker", err)
	}
}

func TestAppRecordingShutdownHandlesAlreadyClosingErrorWriter(t *testing.T) {
	app := &App{}
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	wr := newLogWriter(filepath.Join(blocked, "session"), false, logstream.RecordingOptions{})
	app.registerLogWriter(wr)
	wr.close()
	done := make(chan struct{})
	go func() {
		app.shutdownLogRecordings()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown hung on an already-closing error writer")
	}
	if wr.status().err == "" {
		t.Fatal("test writer did not exercise the error path")
	}
}

func TestWorkbenchWriterBurstUsesEntryAndByteBudgets(t *testing.T) {
	wr := newLogWriter(t.TempDir(), false, logstream.RecordingOptions{})
	defer func() { wr.close(); <-wr.done }()
	entries := make([]logstream.Entry, 4096)
	for i := range entries {
		entries[i] = logstream.Parse(logstream.Source{}, time.Now(), "small burst record")
		entries[i].ID = uint64(i + 1)
	}
	wr.offer(entries)
	if wr.status().dropped != 0 {
		t.Fatalf("4096 small entries dropped despite available8MiB: %+v", wr.status())
	}
	wr.offer([]logstream.Entry{{ID: 4097, Raw: strings.Repeat("x", logWriterQueueBytes)}})
	if wr.status().dropped != 1 {
		t.Fatal("byte limit was not independently enforced", wr.status())
	}
	wr.close()
	select {
	case <-wr.done:
	case <-time.After(5 * time.Second):
		t.Fatal("burst did not drain")
	}
	if state := wr.status(); state.err != "" || state.info.LastID != 4096 {
		t.Fatal("burst not fully recorded", state)
	}
}

func TestWorkbenchWriterBatchesAfterRedactionExpansion(t *testing.T) {
	wr := newLogWriter(t.TempDir(), false, logstream.RecordingOptions{})
	defer func() { wr.close(); <-wr.done }()
	entries := make([]logstream.Entry, 16)
	for i := range entries {
		entries[i] = logstream.Parse(logstream.Source{}, time.Now(), strings.Repeat("Bearer x ", 6000))
		entries[i].ID = uint64(i + 1)
	}
	wr.offer(entries)
	wr.close()
	select {
	case <-wr.done:
	case <-time.After(30 * time.Second):
		t.Fatal("expanded batch did not drain")
	}
	if state := wr.status(); state.err != "" || state.info.LastID != 16 {
		t.Fatal("post-redaction4MiB boundary failed", state)
	}
}

func TestLogClearPreservesFiltersRecordingAndFreshCollapse(t *testing.T) {
	l := NewLog(client.PodGVR, &dao.LogOptions{Path: "ns/p"})
	if err := l.Init(makeContext(t)); err != nil {
		t.Fatal(err)
	}
	w := l.workbench
	w.render()
	if err := w.filter(testErrorQuery); err != nil {
		t.Fatal(err)
	}
	if err := w.filter("-healthz"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	w.writer = newLogWriter(dir, false, logstream.RecordingOptions{})
	defer func() { w.writer.close(); <-w.writer.done }()
	raw := `{"level":"error","message":"repeated failure"}`
	add := func(raw string) {
		w.ingest([]logstream.Entry{logstream.Parse(logstream.Source{Pod: "p"}, time.Now(), raw)})
	}
	add(raw)
	add(raw)
	w.flush()
	w.render()
	oldID := w.rows[0].ID
	// A third identical pending group must be recorded before the display boundary.
	add(raw)
	action, ok := l.logs.Actions().Get(ui.KeyShiftC)
	if !ok {
		t.Fatal("Clear binding missing")
	}
	action.Action(tcell.NewEventKey(tcell.KeyRune, 'C', tcell.ModNone))
	if len(w.rows) != 0 || w.expression != testErrorQuery || len(w.localRules) != 1 {
		t.Fatal("Clear did not clear only display")
	}
	w.resume()
	add(raw)
	add(raw)
	w.flush()
	w.render()
	if len(w.rows) != 1 || w.rows[0].ID <= oldID || w.rows[0].Occurrences != 2 || w.rows[0].Repeats != 2 {
		t.Fatalf("clear hid new collapsed entries or restored old counts: %+v", w.rows)
	}
	w.writer.close()
	<-w.writer.done
	var export strings.Builder
	if _, err := scanLogHistory(context.Background(), dir, 0, nil, &export); err != nil {
		t.Fatal(err)
	}
	if strings.Count(export.String(), " id=") != 5 {
		t.Fatal("clear broke pending/recording continuity", export.String())
	}
}

func TestLogWorkbenchInitializesPresentationSettings(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		showTime, wrap, columnLock bool
	}{
		{name: "disabled"},
		{name: "enabled", showTime: true, wrap: true, columnLock: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := makeContext(t)
			app, err := extractApp(ctx)
			if err != nil {
				t.Fatal(err)
			}
			app.Config.K9s.Logger.ShowTime = tc.showTime
			app.Config.K9s.Logger.TextWrap = tc.wrap
			app.Config.K9s.Logger.ColumnLock = tc.columnLock
			l := NewLog(client.PodGVR, &dao.LogOptions{Path: "ns/p"})
			if err = l.Init(ctx); err != nil {
				t.Fatal(err)
			}
			w := l.workbench
			if w.showTime != tc.showTime || w.wrap != tc.wrap || w.columnLock != tc.columnLock {
				t.Fatalf("workbench settings = time:%t wrap:%t lock:%t", w.showTime, w.wrap, w.columnLock)
			}
		})
	}
}

func TestWorkbenchDiskEvictionVisibleAndExported(t *testing.T) {
	previous := config.AppConfigDir
	config.AppConfigDir = t.TempDir()
	defer func() { config.AppConfigDir = previous }()
	w := testWorkbench(t)
	w.writer = newLogWriter(t.TempDir(), false, logstream.RecordingOptions{MaxBytes: 4096, SegmentBytes: 1024})
	entries := make([]logstream.Entry, 100)
	for i := range entries {
		entries[i] = logstream.Parse(logstream.Source{Pod: "p"}, time.Now(), "retention record")
		entries[i].ID = uint64(i + 1)
	}
	w.writer.offer(entries)
	w.writer.close()
	<-w.writer.done
	state := w.writer.status()
	if state.err != "" || state.dropped != 0 || state.info.EvictedSegments == 0 {
		t.Fatal("expected only disk retention", state)
	}
	w.render()
	if !strings.Contains(w.status.GetText(true), "disk-evict:") || !strings.Contains(w.status.GetText(true), "admission-drop:0") {
		t.Fatal("disk eviction hidden behind queue count", w.status.GetText(true))
	}
	w.showRules()
	if !strings.Contains(w.detail.GetText(true), "disk-evicted-segments=") {
		t.Fatal("inspection omitted retention")
	}
	w.exportDisk()
	deadline := time.Now().Add(2 * time.Second)
	for w.ioRunning && time.Now().Before(deadline) {
		w.consumeIO()
		time.Sleep(time.Millisecond)
	}
	path := strings.TrimPrefix(w.notice, "Safe full disk export: ")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(w.notice, err)
	}
	if !strings.Contains(string(data), fmt.Sprintf("disk-evicted-segments=%d", state.info.EvictedSegments)) || !strings.Contains(string(data), "queue-admission-drops=0") {
		t.Fatal("export omitted actual disk loss", string(data))
	}
}

func TestWorkbenchAcceptedWriterFailureReportsUncertainty(t *testing.T) {
	first := logstream.Parse(logstream.Source{Pod: "p"}, time.Now(), "small")
	first.ID = 1
	encoded, _ := json.Marshal(first)
	wr := newLogWriter(t.TempDir(), false, logstream.RecordingOptions{MaxRecordBytes: len(encoded) + 32})
	entries := make([]logstream.Entry, 512)
	for i := range entries {
		entries[i] = first
		entries[i].ID = uint64(i + 1)
	}
	entries[1] = logstream.Parse(first.Source, time.Now(), strings.Repeat("x", 2048))
	entries[1].ID = 2
	wr.offer(entries)
	wr.close()
	<-wr.done
	if state := wr.status(); state.err == "" || state.dropped != 0 {
		t.Fatal("test must fail after batch/queue admission", state)
	}
	w := testWorkbench(t)
	w.writer = wr
	w.render()
	text := w.status.GetText(true)
	if !strings.Contains(text, "uncertain") || !strings.Contains(text, "admission-drop:0") {
		t.Fatal("failed accepted batch falsely looked lossless", text)
	}
	w.showRules()
	if !strings.Contains(w.detail.GetText(true), "accepted batch/queued records") {
		t.Fatal("inspection omitted failure uncertainty")
	}
}

func TestWorkbenchHistoricalInspectionDoesNotInventZeroLoss(t *testing.T) {
	w := testWorkbench(t)
	w.historyPath = t.TempDir()
	w.showRules()
	text := w.detail.GetText(true)
	if !strings.Contains(text, "counters unavailable for this read-only historical session") {
		t.Fatal("historical inspection did not disclose unavailable loss counters", text)
	}
	if strings.Contains(text, "Loss context: queue-admission-drops=0; disk-evicted-segments=0") {
		t.Fatal("historical inspection invented zero loss counters", text)
	}
}
