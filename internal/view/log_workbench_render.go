// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"fmt"
	"hash/fnv"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/logstream"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
)

//nolint:gocyclo,funlen // Rendering deliberately assembles all mutually exclusive workbench modes in one draw pass.
func (w *logWorkbench) render() {
	w.consumeRecordingStart()
	w.consumeIO()
	w.refreshProfile()
	snapshot := w.liveSnapshot()
	scopeMode := w.mode
	if scopeMode == modeStatus {
		scopeMode = w.statusReturnMode
	}
	historyScope := scopeMode == modeHistory || scopeMode == modeDetail && w.selectionScope != "" && w.selectionScope != scopeLive
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
	viewState := "LIVE"
	if !w.follow {
		viewState = "FROZEN"
	}
	if historyScope {
		viewState = "HISTORY"
	}
	privacy := "Redacted"
	if !w.redact {
		privacy = "Raw display"
	}
	repeats := "Repeats collapsed"
	if !w.collapse {
		repeats = "Repeats expanded"
	}
	grouping := "Multiline grouped"
	if !w.multiline {
		grouping = "Lines separate"
	}
	status := fmt.Sprintf("%s · %s · %s · %s · visible:%d/%d", viewState, privacy, repeats, grouping, shown, observed)
	recording += fmt.Sprintf(" · %s · %s · hidden:%d rules:%d · %s", w.mode, scope, observed-shown, len(w.teamRules)+len(w.localRules), strings.Join(formatText, "/"))
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
	w.statusFull =
		wbText(logstream.SafeText(status)) + "\n" + w.coloredSparkline() +
			wbText(logstream.SafeText(" 60s ≈ · "+recording)) + "\n" + wbText(logstream.SafeText(w.collectorLabel()+" · "+notice))
	privacyShort := "safe"
	if !w.redact {
		privacyShort = "RAW"
	}
	rec := "REC off"
	if w.recordStart != nil {
		rec = "REC starting"
	}
	if state.path != "" {
		rec = "REC on"
		if state.info.Raw {
			rec = "REC RAW"
		}
		if state.closed {
			rec += " closed"
		}
	}
	if state.err != "" {
		rec = "RECORD ERROR: durability unknown"
	}
	if state.info.ConservativeRedaction {
		rec += " conservative"
	}
	third := "Enter detail · s freeze · S status"
	if w.ioRunning {
		third = "Disk running · Esc cancel · S status"
	}
	if state.dropped > 0 || state.info.EvictedSegments > 0 {
		third = fmt.Sprintf("Disk-loss evict:%d drop:%d · S status", state.info.EvictedSegments, state.dropped)
	} else if w.notice != "" && state.err == "" {
		third = "S status · " + w.notice
	}
	if state.err != "" {
		third = "RECORD ERROR: durability unknown · S status"
		rec = "REC failed"
	}
	w.statusCompact = wbText(fmt.Sprintf("%s %s visible:%d/%d · %s\nLoss e:%d t:%d o:%d h:%d · %s\n%s",
		viewState, privacyShort, shown, observed, strings.TrimPrefix(w.collectorLabel(), "Collector "),
		stats.Evicted, stats.Truncated, stats.ForcedOrder, stats.HistogramDropped, rec, third))
	w.paintStreamStatus()
	if w.entryMode() {
		if w.mode == modeHistory {
			visible = w.visible(w.history)
		}
		w.renderEntries(visible)
	}
}

func (w *logWorkbench) paintStreamStatus() {
	_, _, width, _ := w.GetInnerRect()
	text := w.statusFull
	if width >= 40 && width < 110 {
		text = w.statusCompact
	}
	w.status.SetText(text)
}

func (w *logWorkbench) Draw(screen tcell.Screen) {
	_, _, width, _ := w.GetInnerRect()
	if ui.DrawTaskSizeNotice(screen, w.Box) {
		return
	}
	w.paintStreamStatus()
	if w.mode == modeLanes && w.statusWidth != width {
		w.paintLanes()
	}
	w.statusWidth = width
	w.Flex.Draw(screen)
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
func (w *logWorkbench) sourceColor(s logstream.Source) tcell.Color {
	h := fnv.New32a()
	h.Write([]byte(s.Key()))
	palette := w.semanticPalette()
	return []tcell.Color{palette.Category.Color(), palette.Focus.Color(), palette.Progress.Color(), palette.Muted.Color()}[h.Sum32()%4]
}
func (w *logWorkbench) levelColor(level string) tcell.Color {
	palette := w.semanticPalette()
	switch {
	case logstream.Severity(level) >= logstream.Severity("error"):
		return palette.Failure.Color()
	case logstream.Severity(level) >= logstream.Severity("warn"):
		return palette.Warning.Color()
	default:
		return palette.Text.Color()
	}
}
func (w *logWorkbench) headers(headers ...string) {
	w.table.Clear()
	for col, h := range headers {
		w.table.SetCell(0, col, w.newCell(h).SetSelectable(false).SetTextColor(w.semanticPalette().Focus.Color()).SetAttributes(tcell.AttrBold))
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
		w.table.SetCell(row, 0, w.newCell(tm).SetMaxWidth(12))
		w.table.SetCell(row, 1, w.newCell(wbText(sourceName(e.Source))).SetMaxWidth(sourceWidth).SetTextColor(w.sourceColor(e.Source)))
		w.table.SetCell(row, 2, w.newCell(wbText(level)).SetMaxWidth(12).SetTextColor(w.levelColor(e.Level)))
		w.table.SetCell(row, 3, w.newCell(wbText(strings.ReplaceAll(msg, "\n", " ⏎ "))).SetExpansion(1).SetMaxWidth(max(8, width-sourceWidth-28)))
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
	switchStreamPage(w.pages, "table")
}
