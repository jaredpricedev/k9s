// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/logstream"
	"github.com/derailed/k9s/internal/review"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

func desiredReviewSafe(value string) string { return tview.Escape(logstream.Sanitize(value)) }
func desiredReviewEntryKey(entry *review.Entry) string {
	return entry.Identity.APIVersion + ":" + entry.Identity.Kind + ":" + entry.Identity.Namespace + "/" + entry.Identity.Name
}
func desiredReviewMatch(query string, entry *review.Entry) bool {
	all := strings.ToLower(strings.Join([]string{entry.Identity.Kind, entry.Identity.Namespace, entry.Identity.Name, entry.State, entry.Reason}, " "))
	for _, token := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(all, token) {
			return false
		}
	}
	return true
}
func (w *desiredReviewView) render() {
	selected := ""
	row, _ := w.table.GetSelection()
	if row > 0 && row <= len(w.rows) {
		selected = desiredReviewEntryKey(&w.rows[row-1])
	}
	w.rows = nil
	for i := range w.snapshot.Entries {
		entry := &w.snapshot.Entries[i]
		if desiredReviewMatch(w.query, entry) {
			w.rows = append(w.rows, *entry)
		}
	}
	w.renderRows(selected)
	w.renderHeader()
	w.renderDetail()
	w.renderFooter()
}
func (w *desiredReviewView) renderRows(selected string) {
	w.table.Clear()
	palette := w.app.Styles.Semantic()
	canvas := palette.Canvas.Color()
	text := config.ReadableForeground(palette.Text.Color(), canvas)
	width := w.reviewWidth()
	narrow := width < 70
	labels := []string{statusCol, dailyWorkspaceKindCol, "NAMESPACE", "NAME", "CHANGES / COVERAGE"}
	caps := []int{13, 12, 12, max(12, width-64), 0}
	if narrow {
		labels = []string{statusCol, "RESOURCE", "CHANGES / COVERAGE"}
		caps = []int{10, max(12, width-30), 0}
	}
	for col, label := range labels {
		header := tview.NewTableCell(fitInvestigation(label, max(1, caps[col]))).
			SetSelectable(false).SetAttributes(tcell.AttrBold).SetTextColor(palette.Focus.Color())
		w.table.SetCell(0, col, header)
		if caps[col] == 0 {
			w.table.GetCell(0, col).SetText(label).SetExpansion(1)
		}
	}
	selection := 1
	for i := range w.rows {
		entry := &w.rows[i]
		state, summary := w.entrySummary(entry)
		cells := []string{state, entry.Identity.Kind, entry.Identity.Namespace, entry.Identity.Name, summary}
		if narrow {
			cells = []string{state, entry.Identity.Kind + "/" + entry.Identity.Name, summary}
			if entry.State == review.StateChanged || entry.State == review.StateMatch {
				summary = fmt.Sprintf("%d changed; %d unreviewed", len(entry.Intent.Changes), len(entry.Intent.Unreviewed))
				cells[2] = summary
			}
		}
		for col, value := range cells {
			cellWidth := caps[col]
			if cellWidth == 0 {
				cellWidth = max(1, width-len(cells)+1)
				for _, c := range caps {
					cellWidth -= c
				}
			}
			cell := tview.NewTableCell(desiredReviewSafe(fitInvestigation(value, cellWidth))).SetTextColor(text).SetMaxWidth(cellWidth)
			if col == 0 {
				switch entry.State {
				case review.StateChanged, review.StateCreate:
					cell.SetTextColor(palette.Warning.Color())
				case review.StateMatch:
					cell.SetTextColor(palette.Healthy.Color())
				default:
					cell.SetTextColor(palette.Unknown.Color())
				}
			}
			if col == len(cells)-1 {
				cell.SetExpansion(1)
			}
			w.table.SetCell(i+1, col, cell)
		}
		if desiredReviewEntryKey(entry) == selected {
			selection = i + 1
		}
	}
	if len(w.rows) == 0 {
		message := "No search matches. / changes or clears it."
		if w.query == "" {
			message = "n selects source; r refreshes live."
			if w.source.Identity.SHA256 != "" {
				message = "r reads retained source's live targets."
			}
		}
		w.table.Clear()
		w.table.SetCell(0, 0, tview.NewTableCell(fitInvestigation(message, width)).SetSelectable(false).SetExpansion(1))
		selection = 0
	}
	w.table.Select(selection, 0)
}
func (w *desiredReviewView) entrySummary(entry *review.Entry) (state, summary string) {
	if reason, retained := w.retainedReasons[desiredReviewEntryKey(entry)]; retained {
		return "retained", "Observed " + identityTime(entry.ObservedAt) + " · latest read " + reason
	}
	summary = entry.Reason
	if entry.State == review.StateChanged || entry.State == review.StateMatch {
		summary = fmt.Sprintf("%d changes · %d/%d reviewed match · %d unreviewed",
			len(entry.Intent.Changes), entry.Intent.MatchedFields, entry.Intent.DeclaredFields, len(entry.Intent.Unreviewed))
	} else if entry.State == review.StateCreate {
		summary = "Live object not found; local intent only"
	}
	state = entry.State
	if entry.State == review.StateMatch {
		state = "match"
	}
	return state, summary
}
func (w *desiredReviewView) renderDetail() {
	if !w.detailOpen {
		return
	}
	if w.previewOpen && w.serverPreview != nil {
		text := renderServerPreview(w.serverPreview, false, w.reviewWidth())
		if w.evidenceOpen {
			text = w.serverPreviewEvidence
		}
		w.detail.SetText(text)
		return
	}
	entry, ok := w.selectedEntry()
	if !ok {
		text := "This retained detail is unavailable in the current report. Esc returns to the resource table."
		if w.evidenceOpen {
			text = w.renderSourceIdentity() + "\n\n" + desiredReviewSafe("LATEST READ / SOURCE STATUS\n"+w.notice)
		}
		w.detail.SetText(text)
		return
	}
	text := renderDesiredReviewEntryWidth(&entry, w.reviewWidth())
	if w.evidenceOpen {
		scope := "CAPTURED SCOPE\nContext: " + w.contextName + "\nNamespaces: " + strings.Join(w.scope.Namespaces, ", ") +
			"\nKinds: " + strings.Join(w.scope.Kinds, ", ") + "\nSelector: " + w.scope.LabelSelector
		text = w.renderSourceIdentity() + "\n\n" + desiredReviewSafe("LATEST READ / SOURCE STATUS\n"+w.notice) +
			"\n\n" + desiredReviewSafe(scope) + "\n\n" + renderDesiredReviewEntry(&entry)
	}
	if reason, retained := w.retainedReasons[desiredReviewEntryKey(&entry)]; retained {
		text = desiredReviewSafe("RETAINED EVIDENCE · latest read "+reason+"\nOriginal observed time: "+identityTime(entry.ObservedAt)) + "\n\n" + text
	}
	w.detail.SetText(text)
}
func (w *desiredReviewView) renderHeader() {
	source := w.source.Identity
	sourceLine := desiredSourceHeader(&source)
	destination := w.contextName + " · " + strings.Join(w.scope.Namespaces, ", ")
	if w.scope.LabelSelector != "" {
		destination += " · selector " + w.scope.LabelSelector
	}
	if len(w.scope.Kinds) > 0 {
		destination += " · kinds " + strings.Join(w.scope.Kinds, ", ")
	}
	loaded, observed := "not loaded", "not observed"
	if !source.LoadedAt.IsZero() {
		loaded = source.LoadedAt.UTC().Format(time.RFC3339)
	}
	if !w.latestObservedAt.IsZero() {
		observed = w.latestObservedAt.UTC().Format(time.RFC3339)
	}
	changes, match, unknown, candidates, excluded, outScope := 0, 0, 0, 0, 0, 0
	entries := w.latest
	if entries == nil {
		entries = w.snapshot.Entries
	}
	for i := range entries {
		switch entries[i].State {
		case review.StateChanged:
			changes++
		case review.StateMatch:
			match++
		case review.StateCreate:
			candidates++
		case review.StateExcluded:
			excluded++
		case review.StateOutScope:
			outScope++
		default:
			unknown++
		}
	}
	counts := fmt.Sprintf("%d resources · %d changed · %d reviewed matches · %d unavailable", len(w.snapshot.Entries), changes, match, unknown)
	if candidates > 0 {
		counts += fmt.Sprintf(" · %d create candidates", candidates)
	}
	if excluded > 0 {
		counts += fmt.Sprintf(" · %d excluded", excluded)
	}
	if outScope > 0 {
		counts += fmt.Sprintf(" · %d out of scope", outScope)
	}
	if len(w.retainedReasons) > 0 {
		counts += fmt.Sprintf(" · %d retained", len(w.retainedReasons))
	}
	readTimes := "Loaded " + loaded + " · Read " + observed
	notice := w.notice
	if w.query != "" {
		notice = "Search: " + w.query + " · " + notice
	}
	width := w.reviewWidth()
	lines := []string{destination, sourceLine, counts, readTimes, notice}
	if w.detailOpen || width < 70 || w.height > 0 && w.height < 16 {
		fingerprint := source.SHA256[:min(8, len(source.SHA256))]
		freshness := "not observed"
		if !w.snapshot.ObservedAt.IsZero() {
			freshness = w.snapshot.ObservedAt.UTC().Format("15:04:05Z")
		}
		lines = []string{destination, "LOCAL READ ONLY · " + fingerprint + " · observed " + freshness, notice}
	}
	lines = w.serverPreviewHeader(lines, destination)
	for i := range lines {
		lines[i] = desiredReviewSafe(fitInvestigation(lines[i], width))
	}
	w.header.SetText("[::b]" + strings.Join(lines, "\n") + "[::]")
	w.ResizeItem(w.header, len(lines), 0)
}
func desiredSourceHeader(source *review.SourceIdentity) string {
	sourceLine := "Source: not selected"
	if source.SHA256 != "" {
		fingerprint := source.SHA256[:min(12, len(source.SHA256))]
		name := filepath.Base(source.Path)
		if source.Name != "" {
			name = source.Name + " (" + source.Provider + ")"
		}
		sourceLine = fmt.Sprintf("Source: %s · SHA256 %s · %d documents", name, fingerprint, source.Documents)
	}
	return sourceLine
}
func (w *desiredReviewView) renderSourceIdentity() string {
	source := w.source.Identity
	return desiredReviewSafe(fmt.Sprintf("RETAINED SOURCE\nPath: %s\nSHA256: %s\nLoaded: %s · %d bytes · %d documents\nLatest read attempt: %s\nAccepted report: %s",
		source.Path, source.SHA256, identityTime(source.LoadedAt), source.Bytes, source.Documents,
		identityTime(w.latestObservedAt), identityTime(w.snapshot.ObservedAt))) + renderSourceProvenance(&source)
}
func renderDesiredReviewEntry(entry *review.Entry) string {
	var out strings.Builder
	identity := entry.Identity
	if strings.EqualFold(identity.Kind, desiredReviewSecretKind) || identity.GVR.Resource == desiredReviewSecrets {
		return desiredReviewSafe("Secret content excluded. Authored and live values are not displayed or compared.\nEsc returns to the retained resource table.")
	}
	fmt.Fprintf(&out, "%s · %s %s/%s\nState: %s · UID: %s · observed: %s\n",
		identity.Context, identity.Kind, identity.Namespace, identity.Name, entry.State, identity.UID, identityTime(entry.ObservedAt))
	if entry.Reason != "" {
		out.WriteString(entry.Reason + "\n")
	}
	fmt.Fprintf(&out, "\nDECLARED INTENT · %d provided fields · %d matched · %d changes\n",
		entry.Intent.DeclaredFields, entry.Intent.MatchedFields, len(entry.Intent.Changes))
	for _, change := range entry.Intent.Changes {
		fmt.Fprintf(&out, "%s %s\n  live: %s\n  authored: %s\n", strings.ToUpper(change.Kind), change.Path, change.Before, change.After)
	}
	if entry.Intent.Truncated {
		out.WriteString("Changes truncated; this is an incomplete preview.\n")
	}
	out.WriteString("\nUNREVIEWED\nAPI defaulting, admission, omitted live fields and pruning are outside this local comparison.\n")
	for _, item := range entry.Intent.Unreviewed {
		out.WriteString("• " + item + "\n")
	}
	out.WriteString("\nOWNERSHIP EVIDENCE\n")
	if len(entry.Ownership) == 0 {
		out.WriteString("No ownership evidence in this observation.\n")
	} else {
		for _, owner := range entry.Ownership {
			out.WriteString("• " + owner + "\n")
		}
	}
	out.WriteString("\nNo execution, server dry-run or exhaustive deletion plan. Secret values are excluded.\nEsc returns to the same retained table.")
	return desiredReviewSafe(out.String())
}
func identityTime(at time.Time) string {
	if at.IsZero() {
		return workspaceUnknown
	}
	return at.UTC().Format(time.RFC3339)
}

// Review panes support 40×12 including their border. Below that floor, keep
// the retained model and input handlers intact and show a resize/back state.
func (w *desiredReviewView) reviewWidth() int {
	if w.width <= 0 {
		return 78
	}
	return w.width
}
func (w *desiredReviewView) renderFooter() {
	width := w.reviewWidth()
	text := "Enter detail · e evidence · p server preview · / search · Esc back"
	if width < 70 {
		text = "Enter detail · e evidence · p preview · Esc back"
	}
	if width < 48 {
		text = "Enter detail · e evidence · Esc back"
	}
	if w.detailOpen {
		text = "e evidence/source · p preview · Esc table"
		if width < 48 {
			text = "e evidence · p preview · Esc table"
		}
	}
	w.footer.SetText(desiredReviewSafe(fitInvestigation(text, width)))
}
func (w *desiredReviewView) Draw(screen tcell.Screen) {
	_, _, width, height := w.GetInnerRect()
	if ui.DrawTaskSizeNotice(screen, w.Box) {
		return
	}
	if width != w.width || height != w.height {
		w.width, w.height = width, height
		w.render()
	}
	w.Flex.Draw(screen)
}

// Compact detail spends the first screen on values. Exact paths, identities,
// timestamps and ownership remain in the explicit Evidence/source view.
func renderDesiredReviewEntryWidth(entry *review.Entry, width int) string {
	if strings.EqualFold(entry.Identity.Kind, desiredReviewSecretKind) || entry.Identity.GVR.Resource == desiredReviewSecrets {
		return desiredReviewSafe("Secret content excluded. Values are not compared.\ne evidence/source · Esc table")
	}
	var out strings.Builder
	id := entry.Identity
	fmt.Fprintf(&out, "%s %s/%s\n", id.Kind, id.Namespace, id.Name)
	if entry.State == review.StateChanged || entry.State == review.StateMatch || entry.State == review.StateCreate {
		fmt.Fprintf(&out, "%s · %d changes · %d/%d matched · %d unreviewed\n",
			entry.State, len(entry.Intent.Changes), entry.Intent.MatchedFields, entry.Intent.DeclaredFields, len(entry.Intent.Unreviewed))
	} else {
		fmt.Fprintf(&out, "%s · comparison unavailable\n", entry.State)
	}
	if len(entry.Intent.Changes) > 0 {
		out.WriteString("\nCHANGED FIELDS · live → authored\n")
		for _, change := range entry.Intent.Changes {
			path := change.Path
			if width < 90 {
				path = strings.TrimPrefix(path, "/spec/template/spec/")
			}
			fmt.Fprintf(&out, "%s\n  live: %s\n  authored: %s\n", path, change.Before, change.After)
		}
	} else if entry.Reason != "" {
		out.WriteString(entry.Reason + "\n")
	}
	if entry.Intent.Truncated {
		out.WriteString("[~] Changes truncated; preview incomplete.\n")
	}
	out.WriteString("\nLOCAL LIMITS · admission/defaulting/pruning unreviewed\n")
	for _, item := range entry.Intent.Unreviewed {
		out.WriteString(item + "\n")
	}
	out.WriteString("e evidence/source: full paths, identity, time and ownership.\nNothing executed; Secret values excluded.")
	return desiredReviewSafe(out.String())
}

func (w *desiredReviewView) serverPreviewHeader(lines []string, destination string) []string {
	if w.previewOpen && w.serverPreview != nil {
		lines = []string{destination,
			"SERVER DRY RUN · NOT PERSISTED · " + w.serverPreview.ObservedAt.UTC().Format("15:04:05Z"),
			"Retained preview; p invokes again · e evidence/source"}
	}
	return lines
}
