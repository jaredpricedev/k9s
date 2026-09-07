package view

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/logstream"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

const (
	modeEntries  = "entries"
	modeRaw      = "raw"
	modeHistory  = "history"
	modePattern  = "pattern"
	modeDetail   = "detail"
	modeTimeline = "timeline"
	modePatterns = "patterns"
	modeSources  = "sources"
	modeSessions = "sessions"
	modeLanes    = "lanes"
	scopeLive    = "live"
)

// Presentation state belongs to the draw goroutine. Ingestion/recording alone
// use captureMu; it is never held while queuing or executing a draw callback.
type logWorkbench struct {
	recordStart                      *logRecordingStart
	timelineScope, detailReturnMode  string
	timelineEntries, timelineVisible []logstream.Entry
	patternKey                       string
	patternSafe                      bool
	selectionScope                   string
	showTime, columnLock             bool
	timelineBuckets                  []logstream.Bucket
	patternLevel                     string
	owner                            *Log
	*tview.Flex
	table                                       *tview.Table
	detail                                      *tview.TextView
	status                                      *tview.TextView
	pages                                       *tview.Pages
	engine                                      *logstream.Engine
	captureMu                                   sync.Mutex
	writer                                      *logWriter
	stopped                                     atomic.Bool
	unsubscribe                                 func()
	cancel                                      context.CancelFunc
	ioCancel                                    context.CancelFunc
	ioGeneration                                uint64
	ioResults                                   chan logIOResult
	ioRunning                                   bool
	rows                                        []logstream.Entry
	renderedEntries, frozenEntries              []logstream.Entry
	frozenSet                                   bool
	frozenPosition                              logViewPosition
	selected, mark                              uint64
	follow, redact, collapse, multiline, wrap   bool
	mode, expression, notice, isolated, pattern string
	excluded                                    map[string]bool
	query                                       *logstream.Query
	teamRules, localRules                       []string
	scope                                       logstream.Scope
	profileLoaded                               string
	sources                                     []logstream.Source
	laneA, laneB                                string
	patterns                                    []logstream.Pattern
	buckets                                     []logstream.Bucket
	sessions                                    []string
	history                                     []logstream.Entry
	historyPath                                 string
	historyBefore                               uint64
}
type logViewPosition struct {
	selected, mark uint64
	row, column    int
	valid          bool
}
type logIOResult struct {
	generation   uint64
	entries      []logstream.Entry
	sessions     []string
	path, notice string
	err          error
}

func newLogWorkbench(owner *Log, capacity int) *logWorkbench {
	w := &logWorkbench{
		owner: owner, Flex: tview.NewFlex().SetDirection(tview.FlexRow),
		table: tview.NewTable(), detail: tview.NewTextView(), status: tview.NewTextView(), pages: tview.NewPages(),
		engine: logstream.NewEngine(logstream.Options{Capacity: capacity, Collapse: true}),
		follow: true, redact: true, collapse: true, multiline: true, wrap: true, mode: modeEntries,
		excluded: map[string]bool{}, ioResults: make(chan logIOResult, 8),
	}
	w.showTime = true
	w.query, _ = logstream.CompileQuery("", nil)
	w.table.SetSelectable(true, false).SetFixed(1, 0).SetEvaluateAllRows(false)
	w.table.SetInputCapture(w.key)
	w.detail.SetDynamicColors(true).SetWrap(true).SetScrollable(true).SetInputCapture(w.key)
	w.status.SetDynamicColors(true).SetWrap(false)
	w.pages.AddPage("table", w.table, true, true).AddPage(modeDetail, w.detail, true, false)
	w.AddItem(w.status, 3, 0, false).AddItem(w.pages, 0, 1, true)
	w.table.SetSelectionChangedFunc(func(row, _ int) {
		if w.entryMode() && row > 0 && row <= len(w.rows) {
			w.selected = w.rows[row-1].ID
		}
	})
	return w
}
func (w *logWorkbench) entryMode() bool {
	return w.mode == modeEntries || w.mode == modeRaw || w.mode == modeHistory || w.mode == modePattern
}
func (w *logWorkbench) freeze() {
	if w.frozenSet || w.mode == modeHistory {
		return
	}
	if w.renderedEntries != nil {
		w.frozenEntries = w.renderedEntries
	} else {
		w.frozenEntries = w.engine.Snapshot()
	}
	w.frozenSet = true
	w.follow = false
}
func (w *logWorkbench) resume() {
	w.follow = true
	w.frozenEntries = nil
	w.frozenSet = false
	w.frozenPosition = logViewPosition{}
}
func (w *logWorkbench) liveSnapshot() []logstream.Entry {
	if !w.follow {
		w.freeze()
		return w.frozenEntries
	}
	snapshot := w.engine.Snapshot()
	w.renderedEntries = snapshot
	return snapshot
}
func (w *logWorkbench) preserveFrozenPosition() {
	if !w.frozenSet || w.frozenPosition.valid {
		return
	}
	w.frozenPosition.selected = w.selected
	w.frozenPosition.mark = w.mark
	w.frozenPosition.row, w.frozenPosition.column = w.table.GetOffset()
	w.frozenPosition.valid = true
}
func (w *logWorkbench) restoreFrozenPosition() {
	if !w.frozenPosition.valid {
		return
	}
	w.selected = w.frozenPosition.selected
	w.mark = w.frozenPosition.mark
	w.table.SetOffset(w.frozenPosition.row, w.frozenPosition.column)
	w.selectionScope = scopeLive
	w.frozenPosition = logViewPosition{}
}
func wbText(s string) string { return tview.Escape(logstream.Sanitize(s)) }

//nolint:gocritic // Presentation transforms an isolated entry value without mutating retained state.
func (w *logWorkbench) shown(e logstream.Entry) logstream.Entry {
	if w.redact {
		return logstream.SafeEntry(e)
	}
	return e
}
func (w *logWorkbench) ingest(entries []logstream.Entry) {
	w.captureMu.Lock()
	defer w.captureMu.Unlock()
	if w.stopped.Load() {
		return
	}
	for i := range entries {
		w.record(w.engine.AddEntry(entries[i], time.Now()))
	}
}
func (w *logWorkbench) record(entries []logstream.Entry) {
	if w.writer != nil {
		w.writer.offer(entries)
	}
}
func (w *logWorkbench) flush() {
	w.captureMu.Lock()
	defer w.captureMu.Unlock()
	w.record(w.engine.Flush())
}
func (w *logWorkbench) tick() {
	w.captureMu.Lock()
	defer w.captureMu.Unlock()
	if !w.stopped.Load() {
		w.record(w.engine.Tick(time.Now()))
	}
}
func (w *logWorkbench) writerState() logWriterState {
	w.captureMu.Lock()
	r := w.writer
	w.captureMu.Unlock()
	if r == nil {
		return logWriterState{}
	}
	return r.status()
}
func (w *logWorkbench) start() {
	w.stopped.Store(false)
	w.owner.app.registerLogWorkbench(w)
	w.unsubscribe = w.owner.model.SubscribeEntries(w.ingest)
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	go func() {
		timer := time.NewTicker(150 * time.Millisecond)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				w.tick()
				w.owner.app.Application.QueueUpdateDraw(func() {
					if !w.stopped.Load() {
						w.render()
					}
				})
			}
		}
	}()
	// Recording is explicit (R), avoiding any disk I/O during view startup.
	w.notice = "R records safely · ? workbench help · / field query or -noise"
}
func (w *logWorkbench) stop() {
	w.stopped.Store(true)
	w.renderedEntries = nil
	w.frozenEntries = nil
	w.frozenSet = false
	w.frozenPosition = logViewPosition{}
	if w.recordStart != nil {
		w.recordStart.cancel()
	}
	if w.unsubscribe != nil {
		w.unsubscribe()
		w.unsubscribe = nil
	}
	if w.cancel != nil {
		w.cancel()
	}
	if w.ioCancel != nil {
		w.ioCancel()
	}
	w.captureMu.Lock()
	w.record(w.engine.Flush())
	if w.writer != nil {
		w.writer.close()
	}
	w.captureMu.Unlock()
	if w.owner != nil {
		w.owner.app.unregisterLogWorkbench(w)
	}
	// Never join a worker waiting on synchronous QueueUpdateDraw from the UI.
}
func (w *logWorkbench) filter(text string) error {
	text = strings.TrimSpace(text)
	rules := append([]string(nil), w.localRules...)
	expr := text
	if strings.HasPrefix(text, "-") && len(text) > 1 {
		rules = append(rules, strings.TrimSpace(text[1:]))
		expr = w.expression
	}
	merged, err := logstream.MergeRules(w.teamRules, rules)
	if err != nil {
		return err
	}
	q, err := logstream.CompileQuery(expr, merged)
	if err != nil {
		return err
	}
	w.query = q
	w.expression = expr
	w.localRules = rules
	return nil
}
func (w *logWorkbench) refreshProfile() {
	if w.owner == nil {
		return
	}
	opts := w.owner.model.LogOptionsSnapshot()
	scope := logstream.ProfileScope(opts.Context, namespaceOf(opts.Path), opts.Labels, w.owner.app.Config.K9s.Logger.NoiseAppLabels, opts.WorkloadKind, opts.WorkloadName)
	token := fmt.Sprintf("%v/%s", scope, opts.Annotations["k9plus.io/log-noise"])
	if token == w.profileLoaded {
		return
	}
	team, err := logstream.TeamRules(opts.Annotations["k9plus.io/log-noise"])
	if err != nil {
		w.notice = "Team noise profile: " + err.Error()
		w.profileLoaded = token
		return
	}
	local, err := logstream.LoadProfile(w.profilePath(), scope)
	if err != nil {
		w.notice = "Local noise profile: " + err.Error()
		return
	}
	w.teamRules = team
	w.localRules = local
	w.scope = scope
	w.profileLoaded = token
	if err = w.filter(w.expression); err != nil {
		w.notice = err.Error()
	}
}
func namespaceOf(path string) string {
	parts := strings.SplitN(path, "/", 2)
	if len(parts) > 1 {
		return parts[0]
	}
	return ""
}
func (*logWorkbench) profilePath() string {
	return filepath.Join(config.AppConfigDir, "log-noise.json")
}
func (w *logWorkbench) activeRules() []string {
	rules := []string{}
	if w.expression != "" {
		rules = append(rules, w.expression)
	}
	for _, r := range append(append([]string{}, w.teamRules...), w.localRules...) {
		rules = append(rules, "-"+r)
	}
	if w.isolated != "" {
		rules = append(rules, "source="+w.isolated)
	}
	for k := range w.excluded {
		rules = append(rules, "exclude source="+k)
	}
	return rules
}
func (w *logWorkbench) visible(entries []logstream.Entry) []logstream.Entry {
	out := make([]logstream.Entry, 0, len(entries))
	for i := range entries {
		e := &entries[i]
		key := e.Source.Key()
		if w.isolated != "" && key != w.isolated || w.excluded[key] {
			continue
		}
		// Informational provenance survives content/severity filters, but explicit
		// source isolation/exclusion applies to markers as well.
		if e.Marker == nil && !w.query.Match(*e) {
			continue
		}
		if w.mode == modePattern && e.Marker == nil {
			candidate := *e
			if w.patternSafe {
				candidate = logstream.SafeEntry(*e)
			}
			if logstream.EntryPattern(candidate).Key != w.patternKey {
				continue
			}
		}
		out = append(out, *e)
	}
	return out
}

//nolint:gocyclo,funlen // Rendering deliberately assembles all mutually exclusive workbench modes in one draw pass.
func (w *logWorkbench) render() {
	w.consumeRecordingStart()
	w.consumeIO()
	w.refreshProfile()
	snapshot := w.liveSnapshot()
	historyScope := w.mode == modeHistory || w.mode == modeDetail && w.selectionScope != "" && w.selectionScope != scopeLive
	if historyScope {
		w.preserveFrozenPosition()
	} else if w.entryMode() {
		w.restoreFrozenPosition()
	}
	if historyScope {
		snapshot = w.history
	}
	if w.mode == modeTimeline {
		snapshot = w.timelineEntries
		historyScope = w.timelineScope != scopeLive
	}
	stats := w.engine.Stats()
	visible := w.visible(snapshot)
	if w.mode == modeTimeline {
		visible = w.timelineVisible
	}
	var observed, shown uint64
	for i := range snapshot {
		if snapshot[i].Marker == nil {
			observed += snapshot[i].Occurrences
		}
	}
	for i := range visible {
		if visible[i].Marker == nil {
			shown += visible[i].Occurrences
		}
	}
	anchor := time.Now()
	if (historyScope || !w.follow) && len(snapshot) > 0 {
		anchor = snapshot[len(snapshot)-1].RuntimeTime
	}
	if w.mode == modeTimeline {
		w.buckets = append([]logstream.Bucket(nil), w.timelineBuckets...)
	} else {
		w.buckets = workbenchHistogram(snapshot, visible, anchor)
	}
	formats := map[string]int{}
	for i := range snapshot {
		if snapshot[i].Marker == nil {
			formats[snapshot[i].Format]++
		}
	}
	var formatText []string
	for k, n := range formats {
		formatText = append(formatText, fmt.Sprintf("%s:%d", k, n))
	}
	sort.Strings(formatText)
	state := w.writerState()
	recording := "record off (R)"
	if w.recordStart != nil {
		recording = "record starting (R cancels)"
		if w.recordStart.ctx.Err() != nil {
			recording = "record start canceling"
		}
	}
	if state.path != "" && w.recordStart == nil {
		id := fmt.Sprint(state.info.LastID)
		if state.err != "" {
			id = "uncertain"
		}
		recording = fmt.Sprintf(
			"record %s id:%s %dKiB admission-drop:%d disk-evict:%d",
			map[bool]string{true: "RAW", false: "safe"}[state.info.Raw], id,
			state.info.Bytes/1024, state.dropped, state.info.EvictedSegments,
		)
		if state.closed {
			recording += " closed"
		}
		if state.info.ConservativeRedaction {
			recording += " conservative redaction"
		}
		if state.err != "" {
			recording += " ERROR: " + state.err
		}
	}
	scope := "retained live"
	if historyScope {
		scope = "retained disk"
	}
	viewState := "live"
	if !w.follow {
		viewState = "frozen"
	}
	if historyScope {
		viewState = "history"
	}
	status := fmt.Sprintf(
		"%s · %s · view:%s · visible:%d/%d hidden:%d rules:%d · %s · follow:%t(s) safe:%t(d)",
		w.mode, scope, viewState, shown, observed, observed-shown, len(w.teamRules)+len(w.localRules),
		strings.Join(formatText, "/"), w.follow, w.redact,
	)
	recording += fmt.Sprintf(" · collapse:%t(b) group:%t(u)", w.collapse, w.multiline)
	if state.path != "" {
		recording += " · " + filepath.Base(state.path)
	}
	notice := w.notice
	if stats.Evicted > 0 || stats.Truncated > 0 || stats.ForcedOrder > 0 || stats.HistogramDropped > 0 {
		notice = fmt.Sprintf("loss evicted:%d truncated:%d reorder:%d hist:%d · ", stats.Evicted, stats.Truncated, stats.ForcedOrder, stats.HistogramDropped) + notice
	}
	if w.ioRunning {
		notice = "Disk operation running (Esc cancels) · " + notice
	}
	if state.err != "" && w.recordStart == nil {
		notice = "RECORD ERROR: accepted batch/queue durability uncertain; " + state.err
	}
	if state.info.ConservativeRedaction && w.recordStart == nil {
		notice = "Conservative recovery redaction · " + notice
	}
	if w.recordStart == nil && (state.info.EvictedSegments > 0 || state.dropped > 0 || state.err != "") {
		notice = fmt.Sprintf("disk-evict:%d admission-drop:%d · ", state.info.EvictedSegments, state.dropped) + notice
	}
	w.status.SetText(
		wbText(logstream.SafeText(status)) + "\n" + w.coloredSparkline() +
			wbText(logstream.SafeText(" 60s ≈ · "+recording)) + "\n" + wbText(logstream.SafeText(notice)),
	)
	if w.entryMode() {
		if w.mode == modeHistory {
			visible = w.visible(w.history)
		}
		w.renderEntries(visible)
	}
}

//nolint:gocritic // Source formatting consumes an immutable identity value.
func sourceName(s logstream.Source) string {
	pod := s.Pod
	if i := strings.LastIndexByte(pod, '-'); i >= 0 {
		pod = pod[i+1:]
	}
	return fmt.Sprintf("%s/%s#%d", pod, s.Container, s.Generation)
}

//nolint:gocritic // Source hashing consumes an immutable identity value.
func sourceColor(s logstream.Source) tcell.Color {
	h := fnv.New32a()
	h.Write([]byte(s.Key()))
	return []tcell.Color{tcell.ColorAqua, tcell.ColorYellow, tcell.ColorGreen, tcell.ColorFuchsia, tcell.ColorBlue, tcell.ColorOrange}[h.Sum32()%6]
}
func levelColor(level string) tcell.Color {
	switch {
	case logstream.Severity(level) >= logstream.Severity("error"):
		return tcell.ColorRed
	case logstream.Severity(level) >= logstream.Severity("warn"):
		return tcell.ColorYellow
	default:
		return tcell.ColorWhite
	}
}
func (w *logWorkbench) headers(headers ...string) {
	w.table.Clear()
	for col, h := range headers {
		w.table.SetCell(0, col, tview.NewTableCell(h).SetSelectable(false).SetTextColor(tcell.ColorAqua).SetAttributes(tcell.AttrBold))
	}
}
func (w *logWorkbench) renderEntries(entries []logstream.Entry) {
	rowOff, colOff := w.table.GetOffset()
	scope := scopeLive
	if w.mode == modeHistory {
		scope = w.historyPath
	}
	if w.selectionScope != scope {
		w.selected = 0
		w.mark = 0
		rowOff = 0
		colOff = 0
		w.selectionScope = scope
	}
	oldID := w.selected
	w.rows = entries
	w.headers("Time ≈", "Source", "Level", "Message (Enter expands)")
	_, _, width, _ := w.GetInnerRect()
	if width <= 0 {
		width = 100
	}
	sourceWidth := width / 4
	if sourceWidth > 36 {
		sourceWidth = 36
	}
	if sourceWidth < 8 {
		sourceWidth = 8
	}
	selectedRow := 1
	for i := range entries {
		e := w.shown(entries[i])
		row := i + 1
		tm := e.RuntimeTime.Format("15:04:05.000")
		if !w.showTime {
			tm = ""
		}
		msg := e.Message
		level := e.Level
		if w.mode == modeRaw {
			msg = e.Raw
		}
		if e.Marker != nil {
			level = "◆ " + e.Marker.Kind
			msg = fmt.Sprintf("[%s approx=%t] %s", e.Marker.Origin, e.Marker.Approximate, e.Marker.Message)
		}
		if e.Repeats > 1 {
			msg = fmt.Sprintf("×%d %s", e.Repeats, msg)
		}
		if len(e.OriginalLines) > 1 {
			msg = fmt.Sprintf("↳%d lines %s", len(e.OriginalLines), msg)
		}
		if e.Truncated {
			msg = "[TRUNCATED] " + msg
		}
		w.table.SetCell(row, 0, tview.NewTableCell(tm).SetMaxWidth(12))
		w.table.SetCell(row, 1, tview.NewTableCell(wbText(sourceName(e.Source))).SetMaxWidth(sourceWidth).SetTextColor(sourceColor(e.Source)))
		w.table.SetCell(row, 2, tview.NewTableCell(wbText(level)).SetMaxWidth(12).SetTextColor(levelColor(e.Level)))
		w.table.SetCell(row, 3, tview.NewTableCell(wbText(strings.ReplaceAll(msg, "\n", " ⏎ "))).SetExpansion(1).SetMaxWidth(max(8, width-sourceWidth-28)))
		if e.ID == oldID {
			selectedRow = row
		} else if e.ID < oldID {
			selectedRow = row
		}
	}
	if len(entries) > 0 {
		if w.follow && w.mode != modeHistory {
			selectedRow = len(entries)
		}
		selectedRow = min(selectedRow, len(entries))
		w.selected = entries[selectedRow-1].ID
		w.table.Select(selectedRow, 0)
		if w.follow && !w.columnLock {
			w.table.SetOffset(rowOff, 0)
		}
		if !w.follow {
			w.table.SetOffset(rowOff, colOff)
		}
	} else {
		w.selected = 0
	}
	w.pages.SwitchToPage("table")
}
func (w *logWorkbench) selectedEntry() (logstream.Entry, bool) {
	if !w.entryActionsAvailable() {
		return logstream.Entry{}, false
	}
	for i := range w.rows {
		if w.rows[i].ID == w.selected {
			return w.rows[i], true
		}
	}
	return logstream.Entry{}, false
}
func (w *logWorkbench) snippet() string {
	if !w.entryActionsAvailable() {
		return ""
	}
	entries := []logstream.Entry{}
	loss := "retained observed data only; cross-node order approximate"
	if w.mark == 0 {
		if e, ok := w.selectedEntry(); ok {
			entries = append(entries, e)
		}
	} else {
		lo, hi := w.mark, w.selected
		if lo > hi {
			lo, hi = hi, lo
		}
		foundLo, foundHi := false, false
		for i := range w.rows {
			e := &w.rows[i]
			if e.ID == lo {
				foundLo = true
			}
			if e.ID == hi {
				foundHi = true
			}
			if e.ID >= lo && e.ID <= hi {
				entries = append(entries, *e)
			}
		}
		if !foundLo || !foundHi {
			loss += "; marked boundary evicted or excluded; copied available visible entries"
		}
	}
	stats := w.engine.Stats()
	loss += fmt.Sprintf("; evicted=%d truncated=%d", stats.Evicted, stats.Truncated)
	loss += "; " + w.recordingLossContext(w.historyDir())
	return logstream.Snippet(entries, logstream.ExportContext{Filters: w.activeRules(), Loss: loss})
}
func (w *logWorkbench) showText(mode, text string) {
	w.mode = mode
	w.detail.SetText(wbText(text)).ScrollToBeginning()
	w.pages.SwitchToPage(modeDetail)
	w.focus(w.detail)
}

type logDetailPalette struct {
	foreground, key, value, muted, warning, failure string
}

func (w *logWorkbench) logDetailPalette() logDetailPalette {
	styles := config.NewStyles()
	if w.owner != nil && w.owner.app != nil && w.owner.app.Styles != nil {
		styles = w.owner.app.Styles
	}
	return logDetailPalette{
		foreground: styles.Views().Log.FgColor.String(),
		key:        styles.Views().Yaml.KeyColor.String(),
		value:      styles.Views().Yaml.ValueColor.String(),
		muted:      styles.Views().Log.Indicator.ToggleOffColor.String(),
		warning:    styles.K9s.Frame.Status.PendingColor.String(),
		failure:    styles.K9s.Frame.Status.ErrorColor.String(),
	}
}

func detailTag(color, attrs string) string {
	return "[" + color + "::" + attrs + "]"
}

func detailStyled(color, attrs, text string) string {
	return detailTag(color, attrs) + wbText(text) + "[-:-:-]"
}

func detailTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format(time.RFC3339Nano)
}

func detailSeverity(level string, palette *logDetailPalette) string {
	color := ""
	switch logstream.Severity(level) {
	case 4:
		color = palette.warning
	case 5, 6:
		color = palette.failure
	}
	if color == "" {
		return ""
	}
	return "  " + detailStyled(color, "b", strings.ToUpper(level))
}

func detailFields(fields map[string]any, palette *logDetailPalette) string {
	if len(fields) == 0 {
		return detailStyled(palette.muted, "", "(none)")
	}
	return detailJSONValue(fields, palette, 0)
}

func detailJSONValue(value any, palette *logDetailPalette, depth int) string {
	indent := strings.Repeat("  ", depth)
	childIndent := strings.Repeat("  ", depth+1)
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var out strings.Builder
		out.WriteString(detailStyled(palette.foreground, "", "{"))
		for i, key := range keys {
			encodedKey, _ := json.Marshal(key)
			out.WriteByte('\n')
			out.WriteString(childIndent)
			out.WriteString(detailStyled(palette.key, "", string(encodedKey)))
			out.WriteString(detailStyled(palette.foreground, "", ": "))
			out.WriteString(detailJSONValue(typed[key], palette, depth+1))
			if i < len(keys)-1 {
				out.WriteString(detailStyled(palette.foreground, "", ","))
			}
		}
		out.WriteByte('\n')
		out.WriteString(indent)
		out.WriteString(detailStyled(palette.foreground, "", "}"))
		return out.String()
	case []any:
		var out strings.Builder
		out.WriteString(detailStyled(palette.foreground, "", "["))
		for i := range typed {
			out.WriteByte('\n')
			out.WriteString(childIndent)
			out.WriteString(detailJSONValue(typed[i], palette, depth+1))
			if i < len(typed)-1 {
				out.WriteString(detailStyled(palette.foreground, "", ","))
			}
		}
		out.WriteByte('\n')
		out.WriteString(indent)
		out.WriteString(detailStyled(palette.foreground, "", "]"))
		return out.String()
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		encoded = []byte(fmt.Sprint(value))
	}
	return detailStyled(palette.value, "", string(encoded))
}

func renderLogDetail(e *logstream.Entry, palette *logDetailPalette) string {
	var out strings.Builder
	if e.Marker != nil {
		out.WriteString(detailStyled(palette.foreground, "b", "k9+ Notice"))
		out.WriteByte('\n')
		notice := fmt.Sprintf("origin %s | kind %s | time %s | approximate %t",
			e.Marker.Origin, e.Marker.Kind, detailTime(e.Marker.Time), e.Marker.Approximate)
		out.WriteString(detailStyled(palette.muted, "", notice))
		out.WriteByte('\n')
		out.WriteString(detailStyled(palette.foreground, "", e.Marker.Message))
	} else {
		lines := e.OriginalLines
		if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
			lines = []string{e.Raw}
		}
		out.WriteString(detailStyled(palette.foreground, "b", "Original log"))
		out.WriteString(detailSeverity(e.Level, palette))
		out.WriteByte('\n')
		out.WriteString(detailStyled(palette.foreground, "", strings.Join(lines, "\n")))
		out.WriteString("\n\n")
		out.WriteString(detailStyled(palette.foreground, "b", "Parsed fields"))
		out.WriteByte('\n')
		out.WriteString(detailFields(e.Fields, palette))
	}
	out.WriteString("\n\n")
	out.WriteString(detailStyled(palette.muted, "b", "k9+ metadata"))
	out.WriteByte('\n')
	metadata := fmt.Sprintf(
		"entry %d–%d | occurrences %d | repeats %d | format %s | truncated %t\n"+
			"runtime %s → %s | application %s\n"+
			"cluster %s | context %s | namespace %s\n"+
			"pod %s | UID %s | container %s | generation %d\n"+
			"ordering approximate: node clocks",
		e.ID, e.LastID, e.Occurrences, e.Repeats, e.Format, e.Truncated,
		detailTime(e.RuntimeTime), detailTime(e.LastRuntimeTime), detailTime(e.ApplicationTime),
		e.Source.Cluster, e.Source.Context, e.Source.Namespace,
		e.Source.Pod, e.Source.UID, e.Source.Container, e.Source.Generation,
	)
	out.WriteString(detailStyled(palette.muted, "", metadata))
	out.WriteString("\n\n")
	out.WriteString(detailStyled(palette.muted, "b", "Controls"))
	out.WriteByte('\n')
	out.WriteString(detailStyled(palette.muted, "", "Esc return | w wrap | c copy safely | m mark range boundary"))
	return out.String()
}

func (w *logWorkbench) showDetail(e *logstream.Entry) {
	w.mode = modeDetail
	palette := w.logDetailPalette()
	w.detail.SetText(renderLogDetail(e, &palette)).ScrollToBeginning()
	w.pages.SwitchToPage(modeDetail)
	w.focus(w.detail)
}
func (w *logWorkbench) focus(p tview.Primitive) {
	if w.owner != nil {
		w.owner.app.SetFocus(p)
	}
}
func (w *logWorkbench) expand() {
	row, _ := w.table.GetSelection()
	switch w.mode {
	case modePatterns:
		if row > 0 && row <= len(w.patterns) {
			w.pattern = w.patterns[row-1].Template
			w.patternLevel = w.patterns[row-1].Level
			w.patternKey = w.patterns[row-1].Key
			w.mode = modePattern
			w.freeze()
			w.render()
		}
		return
	case modeSources:
		w.chooseLane(row)
		return
	case modeTimeline:
		if row > 0 && row <= len(w.timelineBuckets) {
			w.buckets = append([]logstream.Bucket(nil), w.timelineBuckets...)
			w.jumpBucket(row - 1)
		}
		return
	case modeSessions:
		if row > 0 && row <= len(w.sessions) {
			w.historyPath = w.sessions[row-1]
			w.historyBefore = 0
			w.readHistory(false)
		}
		return
	}
	if !w.entryActionsAvailable() {
		w.unavailableEntryAction()
		return
	}
	e, ok := w.selectedEntry()
	if !ok {
		return
	}
	if w.mode != modeDetail {
		w.detailReturnMode = w.mode
	}
	w.freeze()
	e = w.shown(e)
	w.showDetail(&e)
}
func (w *logWorkbench) showPatterns() {
	w.freeze()
	entries := w.visible(w.liveSnapshot())
	w.patternSafe = w.redact
	for i := range entries {
		entries[i] = w.shown(entries[i])
	}
	w.patterns = logstream.TopPatterns(entries, 100)
	w.mode = modePatterns
	w.headers("Count", "Level", "Pattern (Enter drills down)")
	for i, p := range w.patterns {
		w.table.SetCell(i+1, 0, tview.NewTableCell(fmt.Sprint(p.Count)))
		w.table.SetCell(i+1, 1, tview.NewTableCell(wbText(p.Level)))
		w.table.SetCell(i+1, 2, tview.NewTableCell(wbText(p.Template)).SetExpansion(1))
	}
	w.table.Select(1, 0)
	w.pages.SwitchToPage("table")
	w.focus(w.table)
}
func (w *logWorkbench) showSources() {
	w.freeze()
	seen := map[string]bool{}
	w.sources = nil
	entries := w.liveSnapshot()
	for i := range entries {
		e := &entries[i]
		if e.Source.Pod == "" {
			continue
		}
		k := e.Source.Key()
		if !seen[k] {
			seen[k] = true
			w.sources = append(w.sources, e.Source)
		}
	}
	sort.Slice(w.sources, func(i, j int) bool { return w.sources[i].Key() < w.sources[j].Key() })
	w.mode = modeSources
	w.headers("Source (Enter selects lane A, then B)", "Lane")
	for i, s := range w.sources {
		lane := ""
		if s.Key() == w.laneA {
			lane = "A"
		}
		if s.Key() == w.laneB {
			lane = "B"
		}
		w.table.SetCell(i+1, 0, tview.NewTableCell(wbText(logstream.SafeText(sourceName(s)))).SetTextColor(sourceColor(s)).SetExpansion(1))
		w.table.SetCell(i+1, 1, tview.NewTableCell(lane))
	}
	w.table.Select(1, 0)
	w.pages.SwitchToPage("table")
	w.focus(w.table)
}
func (w *logWorkbench) chooseLane(row int) {
	if row <= 0 || row > len(w.sources) {
		return
	}
	key := w.sources[row-1].Key()
	if w.laneA == "" || w.laneB != "" {
		w.laneA = key
		w.laneB = ""
		w.notice = "Lane A selected; choose another source and Enter"
		w.showSources()
		return
	}
	if key == w.laneA {
		w.notice = "Choose a different source for lane B"
		return
	}
	w.laneB = key
	w.showLanes()
}
func (w *logWorkbench) showLanes() {
	var a, b []logstream.Entry
	entries := w.visible(w.liveSnapshot())
	for i := range entries {
		e := &entries[i]
		if e.Source.Key() == w.laneA {
			a = append(a, *e)
		}
		if e.Source.Key() == w.laneB {
			b = append(b, *e)
		}
	}
	w.mode = modeLanes
	w.headers("Lane A · chronological retained entries", "Lane B · independent node clock")
	_, _, width, _ := w.GetInnerRect()
	if width <= 0 {
		width = 100
	}
	for i := range max(len(a), len(b)) {
		for col, entries := range [][]logstream.Entry{a, b} {
			if i >= len(entries) {
				continue
			}
			e := w.shown(entries[i])
			cell := tview.NewTableCell(wbText(e.RuntimeTime.Format("15:04:05") + " " + sourceName(e.Source) + " " + e.Message))
			w.table.SetCell(i+1, col, cell.SetMaxWidth(max(10, width/2-2)).SetExpansion(1))
		}
	}
	w.table.Select(1, 0)
	w.pages.SwitchToPage("table")
	w.focus(w.table)
	w.notice = "Two source lanes; row alignment does not imply simultaneous events. g refreshes; v chooses sources"
}
func (w *logWorkbench) showTimeline() {
	if w.mode != modeTimeline {
		w.timelineScope = scopeLive
		if w.mode == modeHistory || w.mode == modeDetail && w.selectionScope != scopeLive && w.selectionScope != "" {
			w.timelineScope = w.historyPath
		}
	}
	snapshot := w.liveSnapshot()
	anchor := time.Now()
	if w.timelineScope != scopeLive {
		snapshot = w.history
		if len(snapshot) > 0 {
			anchor = snapshot[len(snapshot)-1].RuntimeTime
		}
	}
	w.timelineEntries = snapshot
	w.timelineVisible = w.visible(snapshot)
	w.timelineBuckets = workbenchHistogram(snapshot, w.timelineVisible, anchor)
	w.buckets = append([]logstream.Bucket(nil), w.timelineBuckets...)
	w.freeze()
	w.mode = modeTimeline
	w.headers("Second ≈", "Observed", "Visible", "Severity (Enter jumps)")
	for i, b := range w.buckets {
		w.table.SetCell(i+1, 0, tview.NewTableCell(b.Start.Format("15:04:05")))
		w.table.SetCell(i+1, 1, tview.NewTableCell(fmt.Sprint(b.Observed)))
		w.table.SetCell(i+1, 2, tview.NewTableCell(fmt.Sprint(b.Visible)))
		w.table.SetCell(i+1, 3, tview.NewTableCell(fmt.Sprint(b.Severity)))
	}
	w.table.Select(60, 0)
	w.pages.SwitchToPage("table")
	w.focus(w.table)
	w.notice = "60 one-second buckets of retained observed data; J jumps to largest spike"
}
func (w *logWorkbench) jumpBucket(i int) {
	if i < 0 || i >= len(w.buckets) || w.buckets[i].EntryID == 0 {
		w.notice = "No visible retained entry in this bucket; clear filters to jump"
		return
	}
	target := w.buckets[i].EntryID
	scope := scopeLive
	if w.mode == modeTimeline {
		scope = w.timelineScope
		if scope != scopeLive {
			w.history = w.timelineEntries
			w.historyPath = scope
		}
	} else if w.mode == modeHistory || w.mode == modeDetail && w.selectionScope != scopeLive && w.selectionScope != "" {
		scope = w.historyPath
	}
	if w.selectionScope != scope {
		w.mark = 0
		w.selectionScope = scope
	}
	w.selected = target
	w.mode = modeEntries
	if scope != scopeLive {
		w.mode = modeHistory
	}
	w.freeze()
	w.render()
	w.focus(w.table)
}

const logWorkbenchHelp = `Log workbench · capture continues in every mode

/      typed AND query or raw regex; Enter applies; invalid query keeps prior results
       Example: level>=error AND http.status>=500
       Submit -healthz, then -readiness to stack literal exclusions
Enter  full-width selected entry detail / pattern drilldown / choose bucket or session
Esc/q  cancel prompt or disk operation; return to live entries; then leave logs
↑/↓, j/k, PgUp/PgDn, Home/End  freeze displayed entries and navigate; s toggles frozen/live
       Capture and recording continue while the displayed entry snapshot is frozen.
c      copy selected entry or m-marked range, safely, with provenance and filters
       Entry/copy/mark/visible export apply to entry tables or detail, not selectors.
m      mark/unmark the selected stable ID; missing boundaries are reported
Ctrl-S export visible retained entries safely
C      clear live display; preserve query/noise, IDs, privacy state and recording
E      export ALL retained selected disk session safely, in background

o      raw message / structured columns (redaction still applies)
d      toggle DISPLAY redaction only; copy/export always safe
b      collapse future identical grouped entries (existing collapsed rows retained)
u      toggle source-local multiline grouping, flush pending groups
w      wrap full-width detail (scope: detail/help text)
p      top 100 retained patterns; Enter drills down
i/x/z  isolate / exclude selected source / reset source filters
v      choose lane A then a different lane B with Enter; g refreshes frozen lanes
h      retained 60-second severity histogram; Enter jumps to selected bucket
J      jump to largest retained spike
n      inspect active query, team and local noise expressions
N      save local noise profile for context/namespace/app label
X      clear and save local noise rules; team annotation remains
R      start/stop a NEW bounded SAFE recording; R/Esc cancels preparation
Ctrl-R explicitly start a NEW RAW recording (separate from display redaction)
[      page older recorded entries (200/page); ] latest recorded page
D      search selected recording using current / query (200 earliest matches)
O      resume picker: inspect old recordings READ-ONLY while live capture continues
G      prompt for RFC3339 server sinceTime (available server history only)
g      refresh the current secondary view; Esc returns to live entries
?      this help (in place; collection and recording continue)
0–6    existing tail/head/time windows; a all containers when available
f/t/L  existing fullscreen / timestamps / column lock controls

Markers identify Kubernetes Event, source/status or client origin and approximation.
Content/severity filters preserve markers; explicit source filters include markers.
Counts cover retained observed lines, including collapsed/grouped occurrences.
Cross-node order and Events are approximate. Disk only contains observed entries.
Server jump reconnects with sinceTime: PodLogs cannot seek; rotation limits history.
Redaction is heuristic. ANSI/control characters are neutralized in all display modes.
Recording status separates admission drops, disk eviction and failure uncertainty.
Record path and loss context: press n.
`

//nolint:gocyclo,funlen // One key router keeps shortcut precedence and modal behavior auditable in one place.
func (w *logWorkbench) key(evt *tcell.EventKey) *tcell.EventKey {
	if w.owner != nil && w.owner.logs.cmdBuff.IsActive() {
		if evt.Key() == tcell.KeyEnter {
			return w.owner.filterCmd(evt)
		}
		if evt.Key() == tcell.KeyEscape {
			return w.owner.resetCmd(evt)
		}
		return evt
	}
	key := evt.Rune()
	if evt.Key() == tcell.KeyEscape || key == 'q' {
		if w.recordStart != nil {
			w.recordStart.cancel()
			w.notice = "Recording start canceling"
			return nil
		}
		if w.ioRunning && w.ioCancel != nil {
			w.ioCancel()
			w.ioGeneration++
			w.ioRunning = false
			w.notice = "Disk operation canceled"
			return nil
		}
		if w.mode != modeEntries {
			previous := w.mode
			w.mode = modeEntries
			if previous == modeDetail && w.detailReturnMode != "" {
				w.mode = w.detailReturnMode
			} else if previous == modeTimeline && w.timelineScope != scopeLive {
				w.mode = modeHistory
				w.historyPath = w.timelineScope
			}
			if previous == modeHistory {
				w.historyPath = ""
			}
			w.render()
			w.focus(w.table)
			return nil
		}
		if w.owner != nil {
			return w.owner.resetCmd(evt)
		}
		return nil
	}
	if evt.Key() == tcell.KeyEnter {
		w.expand()
		return nil
	}
	if evt.Key() == tcell.KeyCtrlR {
		w.startRecording(true)
		return nil
	}
	if (evt.Key() == tcell.KeyCtrlS || key == 'c' || key == 'm') && !w.entryActionsAvailable() {
		w.unavailableEntryAction()
		return nil
	}
	if evt.Key() == tcell.KeyCtrlS {
		w.exportVisible()
		return nil
	}
	switch key {
	case '?':
		w.freeze()
		w.showText("help", logWorkbenchHelp)
	case 'o':
		if w.mode == modeRaw {
			w.mode = modeEntries
		} else {
			w.mode = modeRaw
		}
		w.render()
		w.focus(w.table)
	case 'd':
		w.redact = !w.redact
		w.notice = fmt.Sprintf("Display redaction %t; copy/export remain safe; recording mode unchanged", w.redact)
		switch w.mode {
		case modeDetail:
			w.expand()
		case modePatterns:
			w.showPatterns()
		case modeLanes:
			w.showLanes()
		}
		w.render()
	case 'b':
		w.collapse = !w.collapse
		w.engine.SetCollapse(w.collapse)
		w.notice = "Collapse changes future commits; existing summaries retain identity"
	case 'u':
		w.multiline = !w.multiline
		w.captureMu.Lock()
		w.record(w.engine.SetMultiline(w.multiline))
		w.captureMu.Unlock()
	case 's':
		if w.follow {
			w.freeze()
		} else {
			w.resume()
		}
		w.mode = modeEntries
		w.render()
		w.focus(w.table)
	case 'w':
		w.wrap = !w.wrap
		w.detail.SetWrap(w.wrap)
		w.notice = fmt.Sprintf("Full-width detail wrapping %t (Enter expands a row)", w.wrap)
	case 'p':
		w.showPatterns()
	case 'v':
		w.showSources()
	case 'h':
		w.showTimeline()
	case 'J':
		best := 0
		for i, b := range w.buckets {
			if b.Observed > w.buckets[best].Observed {
				best = i
			}
		}
		w.jumpBucket(best)
	case 'i', 'x':
		if e, ok := w.selectedEntry(); ok {
			if key == 'i' {
				w.isolated = e.Source.Key()
			} else {
				w.excluded[e.Source.Key()] = true
			}
			w.render()
		}
	case 'z':
		w.isolated = ""
		w.excluded = map[string]bool{}
		w.render()
	case 'm':
		if w.mark == w.selected {
			w.mark = 0
		} else {
			w.mark = w.selected
		}
		w.notice = fmt.Sprintf("Range boundary ID %d; c copies to selection safely", w.mark)
	case 'c':
		if err := clipboardWrite(w.snippet()); err != nil {
			w.notice = "Copy: " + err.Error()
		} else {
			w.notice = "Copied safely with source, filters and loss metadata"
		}
	case 'n':
		w.showRules()
	case 'N':
		w.saveProfile(false)
	case 'X':
		w.saveProfile(true)
	case 'R':
		w.startRecording(false)
	case '[':
		w.readHistory(false)
	case ']':
		w.historyBefore = 0
		w.readHistory(false)
	case 'D':
		w.historyBefore = 0
		w.readHistory(true)
	case 'O':
		w.openSessions()
	case 'E':
		w.exportDisk()
	case 'G':
		if w.owner != nil {
			w.owner.promptPurpose = "sinceTime"
			w.notice = "Enter RFC3339 timestamp; available server history only"
			w.owner.app.ResetPrompt(w.owner.logs.cmdBuff)
		}
	case 'g':
		switch w.mode {
		case modePatterns:
			w.showPatterns()
		case modeSources:
			w.showSources()
		case modeLanes:
			w.showLanes()
		case modeTimeline:
			w.showTimeline()
		case modeSessions:
			w.openSessions()
		case "rules":
			w.showRules()
		default:
			w.render()
		}
	default:
		navigationKey := evt.Key() == tcell.KeyUp || evt.Key() == tcell.KeyDown ||
			evt.Key() == tcell.KeyPgUp || evt.Key() == tcell.KeyPgDn ||
			evt.Key() == tcell.KeyHome || evt.Key() == tcell.KeyEnd || key == 'j' || key == 'k'
		if navigationKey {
			w.freeze()
			return evt
		}
		if w.owner != nil {
			return w.owner.logs.keyboard(evt)
		}
		return evt
	}
	return nil
}
func (w *logWorkbench) showRules() {
	state := w.writerState()
	text := fmt.Sprintf(
		"Active query: %s\nScope: %+v\n\nTeam rules (annotation k9plus.io/log-noise):\n%s\n\n"+
			"Local rules:\n%s\n\nSource filters:\n%s\n\nRecording: %s\n"+
			"Raw: %t; bytes: %d; last processed ID (not a durability count): %d\n"+
			"Loss context: %s\nError: %s\n\nSelected history: %s\n\n"+
			"N saves local rules; X resets local rules; / -term stacks a literal exclusion.\n"+
			"Content/severity queries retain markers. z resets source filters.",
		w.expression, w.scope, strings.Join(w.teamRules, "\n"), strings.Join(w.localRules, "\n"),
		strings.Join(w.activeRules(), "\n"), state.path, state.info.Raw, state.info.Bytes, state.info.LastID,
		w.recordingLossContext(w.historyDir()), state.err, w.historyPath,
	)
	w.showText("rules", logstream.SafeText(text))
}
func (w *logWorkbench) saveProfile(reset bool) {
	if reset {
		w.localRules = nil
		if err := w.filter(w.expression); err != nil {
			w.notice = err.Error()
			return
		}
	}
	if err := logstream.SaveProfile(w.profilePath(), w.scope, w.localRules); err != nil {
		w.notice = "Noise profile: " + err.Error()
	} else {
		w.notice = "Local noise profile saved"
	}
	if w.mode == "rules" {
		w.showRules()
	}
}
func (*logWorkbench) recordingRoot() string {
	return filepath.Join(config.AppConfigDir, "log-recordings")
}
func (w *logWorkbench) startRecording(raw bool) {
	if w.stopped.Load() {
		w.notice = "Log capture is stopped"
		return
	}
	if op := w.recordStart; op != nil {
		if op.ctx.Err() == nil {
			op.cancel()
			w.notice = "Recording start canceling"
			return
		}
		select {
		case <-op.done:
			w.recordStart = nil
		default:
			w.notice = "Recording start canceling; wait for maintenance to finish"
			return
		}
	}
	w.captureMu.Lock()
	old := w.writer
	if old != nil && !old.status().closed {
		old.close()
		w.captureMu.Unlock()
		if !raw {
			w.notice = "Recording closing; queued entries drain in background"
			return
		}
		w.notice = "Wait for safe recording to close, then Ctrl-R explicitly starts raw recording"
		return
	}
	w.captureMu.Unlock()
	maxSessions, hours := 8, 24
	opts := logstream.RecordingOptions{}
	if w.owner != nil {
		cfg := w.owner.app.Config.K9s.Logger
		maxSessions = cfg.RecordingSessions
		hours = cfg.RecordingRetentionHours
		opts.MaxBytes = int64(cfg.RecordingMaxMiB) << 20
		opts.Retention = time.Duration(hours) * time.Hour
	}
	w.recordStart = newRecordingStart(w.recordingRoot(), maxSessions, hours, raw, opts)
	if w.owner != nil && !w.owner.app.registerLogStart(w.recordStart) {
		w.recordStart = nil
		w.notice = "Recording start canceled during application shutdown"
		return
	}
	w.notice = "Recording starting in background; R cancels"
}
func (w *logWorkbench) historyDir() string {
	if w.historyPath != "" {
		return w.historyPath
	}
	return w.writerState().path
}
func (w *logWorkbench) beginIO(work func(context.Context) logIOResult) {
	if w.ioCancel != nil {
		w.ioCancel()
	}
	w.ioGeneration++
	gen := w.ioGeneration
	ctx, cancel := context.WithCancel(context.Background())
	w.ioCancel = cancel
	w.ioRunning = true
	go func() {
		result := work(ctx)
		result.generation = gen
		select {
		case w.ioResults <- result:
		case <-ctx.Done():
		}
	}()
}
func (w *logWorkbench) readHistory(search bool) {
	dir := w.historyDir()
	if dir == "" {
		w.notice = "No recording selected; R starts safe recording, O opens a session"
		return
	}
	before := w.historyBefore
	var q *logstream.Query
	if search {
		q = w.query
	}
	w.freeze()
	w.beginIO(func(ctx context.Context) logIOResult {
		entries, err := scanLogHistory(ctx, dir, before, q, nil)
		notice := "Recorded history (200/page); [ older, ] latest; Esc live"
		if errors.Is(err, errLogHistoryTornTail) {
			notice += "; torn final record ignored; durability uncertain"
			err = nil
		}
		return logIOResult{entries: entries, path: dir, notice: notice, err: err}
	})
}
func (w *logWorkbench) openSessions() {
	root := w.recordingRoot()
	w.beginIO(func(ctx context.Context) logIOResult {
		paths, err := listLogSessionsContext(ctx, root)
		if paths == nil {
			paths = []string{}
		}
		return logIOResult{sessions: paths, notice: "Read-only session picker; Enter opens; live capture continues", err: err}
	})
}
func (w *logWorkbench) consumeIO() {
	for {
		select {
		case result := <-w.ioResults:
			if result.generation != w.ioGeneration {
				continue
			}
			w.ioRunning = false
			if result.err != nil {
				w.notice = "Disk ERROR: " + result.err.Error()
				continue
			}
			w.notice = result.notice
			if result.sessions != nil {
				w.sessions = result.sessions
				w.mode = modeSessions
				w.headers("Recorded session (read-only)")
				for i, path := range w.sessions {
					w.table.SetCell(i+1, 0, tview.NewTableCell(wbText(path)).SetExpansion(1))
				}
				w.table.Select(1, 0)
				w.pages.SwitchToPage("table")
				w.focus(w.table)
			} else if result.path != "" {
				w.historyPath = result.path
				w.history = result.entries
				w.mode = modeHistory

				if len(result.entries) > 0 {
					w.historyBefore = result.entries[0].ID
				}
				w.focus(w.table)
			}
		default:
			return
		}
	}
}
func (w *logWorkbench) exportVisible() {
	if !w.entryActionsAvailable() {
		w.unavailableEntryAction()
		return
	}
	if w.owner == nil {
		return
	}
	entries := append([]logstream.Entry(nil), w.rows...)
	if w.mode == modeDetail {
		entries = nil
		if e, ok := w.selectedEntry(); ok {
			entries = []logstream.Entry{e}
		}
	}
	exportContext := logstream.ExportContext{
		Filters: w.activeRules(),
		Loss:    "visible retained entries only; cross-node order approximate; " + w.recordingLossContext(w.historyDir()),
	}
	dir := w.owner.app.Config.K9s.ContextScreenDumpDir()
	target := filepath.Join(dir, fmt.Sprintf("log-visible-%d.log", time.Now().UnixNano()))
	w.beginIO(func(ctx context.Context) logIOResult {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return logIOResult{err: err}
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return logIOResult{err: err}
		}
		err = logstream.Export(contextLogWriter{ctx: ctx, writer: f}, entries, exportContext)
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(target)
		}
		return logIOResult{notice: "Safe visible export: " + target, err: err}
	})
}
func (w *logWorkbench) exportDisk() {
	dir := w.historyDir()
	if dir == "" {
		w.notice = "Select/start a recording first"
		return
	}
	targetDir := filepath.Join(config.AppConfigDir, "screen-dumps")
	if w.owner != nil {
		targetDir = w.owner.app.Config.K9s.ContextScreenDumpDir()
	}
	target := filepath.Join(targetDir, fmt.Sprintf("log-export-%d.log", time.Now().UnixNano()))
	exportContext := logstream.ExportContext{Loss: w.recordingLossContext(dir)}
	w.beginIO(func(ctx context.Context) logIOResult {
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return logIOResult{err: err}
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return logIOResult{err: err}
		}
		_, err = scanLogHistory(ctx, dir, 0, nil, f, exportContext)
		notice := "Safe full disk export: " + target
		if errors.Is(err, errLogHistoryTornTail) {
			notice = "Safe full disk export with torn-tail recovery uncertainty: " + target
			err = nil
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			os.Remove(target)
		}
		return logIOResult{notice: notice, err: err}
	})
}

// register adds discoverable hints while a single capture function guards the
// active filter prompt. Existing time/container/fullscreen actions stay shared.
func (w *logWorkbench) register() {
	actions := []struct {
		r     rune
		label string
	}{
		{'?', "Workbench Help"}, {'o', "Raw/Structured"}, {'d', "Display Redaction"}, {'b', "Collapse"},
		{'u', "Multiline"}, {'p', "Patterns"}, {'v', "Source Lanes"}, {'h', "Timeline"}, {'J', "Jump Spike"},
		{'i', "Isolate Source"}, {'x', "Exclude Source"}, {'z', "Reset Sources"}, {'n', "Inspect Rules/Record"},
		{'N', "Save Noise Profile"}, {'X', "Reset Noise Profile"}, {'R', "Safe Recording"}, {'O', "Resume Recording"},
		{'D', "Disk Search"}, {'E', "Safe Disk Export"}, {'G', "Server Timestamp"}, {'[', "Older History"},
		{']', "Latest History"},
	}
	for _, item := range actions {
		r := item.r
		key := ui.AsKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
		action := ui.NewKeyAction(item.label, func(evt *tcell.EventKey) *tcell.EventKey { return w.key(evt) }, false)
		w.owner.logs.Actions().Add(key, action)
	}
}

func (w *logWorkbench) coloredSparkline() string {
	symbols := []rune("▁▂▃▄▅▆▇█")
	var maxCount uint64
	for _, b := range w.buckets {
		if b.Observed > maxCount {
			maxCount = b.Observed
		}
	}
	var out strings.Builder
	// Thirty cells aggregate pairs visually; the selectable timeline keeps all60
	// individual one-second buckets and their exact counts/entry targets.
	for n := 0; n < len(w.buckets); n += 2 {
		b := w.buckets[n]
		if n+1 < len(w.buckets) {
			next := w.buckets[n+1]
			if next.Observed > b.Observed {
				b.Observed = next.Observed
			}
			if next.Severity > b.Severity {
				b.Severity = next.Severity
			}
		}
		i := uint64(0)
		if maxCount > 0 {
			i = b.Observed * 7 / maxCount
		}
		color := "gray"
		if b.Observed > 0 {
			color = "green"
		}
		if b.Severity >= logstream.Severity("warn") {
			color = "yellow"
		}
		if b.Severity >= logstream.Severity("error") {
			color = "red"
		}
		fmt.Fprintf(&out, "[%s::]%c[-::]", color, symbols[i])
	}
	return out.String()
}

// Build the histogram from exactly the retained snapshot used by the table,
// including source/pattern filters and the selected disk session's clock range.
func workbenchHistogram(all, visible []logstream.Entry, now time.Time) []logstream.Bucket {
	buckets := make([]logstream.Bucket, 60)
	start := now.Truncate(time.Second).Add(-59 * time.Second)
	for i := range buckets {
		buckets[i].Start = start.Add(time.Duration(i) * time.Second)
	}
	included := map[uint64]bool{}
	for i := range visible {
		included[visible[i].ID] = true
	}
	for i := range all {
		e := &all[i]
		if e.Marker != nil {
			continue
		}
		timeline := e.Timeline
		if len(timeline) == 0 {
			timeline = []logstream.TimeCount{{Time: e.RuntimeTime, Count: e.Occurrences}}
		}
		for _, tc := range timeline {
			idx := int(tc.Time.Truncate(time.Second).Sub(start) / time.Second)
			if tc.Time.Before(start) || idx < 0 || idx >= len(buckets) {
				continue
			}
			b := &buckets[idx]
			b.Observed += tc.Count
			if included[e.ID] {
				b.Visible += tc.Count
				if b.EntryID == 0 {
					b.EntryID = e.ID
				}
			}
			if severity := logstream.Severity(e.Level); severity > b.Severity {
				b.Severity = severity
			}
		}
	}
	return buckets
}

func (w *logWorkbench) entryActionsAvailable() bool { return w.entryMode() || w.mode == modeDetail }
func (w *logWorkbench) unavailableEntryAction() {
	w.notice = "entry action unavailable in " + w.mode + "; open an entry table or detail first (Esc returns)"
}

func (w *logWorkbench) clearRetained() {
	w.captureMu.Lock()
	w.record(w.engine.ClearRetained())
	w.captureMu.Unlock()
	w.mode = modeEntries
	w.renderedEntries = nil
	w.frozenEntries = nil
	w.frozenSet = false
	w.frozenPosition = logViewPosition{}
	w.rows = nil
	w.selected = 0
	w.mark = 0
	w.selectionScope = scopeLive
	w.table.SetOffset(0, 0).Select(1, 0)
	w.timelineEntries = nil
	w.timelineVisible = nil
	w.timelineBuckets = nil
	w.notice = "Live display cleared; query/noise, IDs, privacy state and recording preserved"
	w.render()
	w.focus(w.table)
}
func (w *logWorkbench) recordingLossContext(dir string) string {
	state := w.writerState()
	if dir != "" && dir == state.path {
		return state.lossContext()
	}
	if dir != "" {
		return "recording counters unavailable for this read-only historical session; retention may have removed segments"
	}
	return "no recording selected"
}
