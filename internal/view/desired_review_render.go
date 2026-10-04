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
	w.footer.SetText("Enter detail · r refresh live · n select/reload source · / local search · i investigate live · Esc back")
}
func (w *desiredReviewView) renderRows(selected string) {
	w.table.Clear()
	palette := w.app.Styles.Semantic()
	canvas := palette.Canvas.Color()
	text := config.ReadableForeground(palette.Text.Color(), canvas)
	for col, label := range []string{statusCol, dailyWorkspaceKindCol, "NAMESPACE", "NAME", "CHANGES / COVERAGE"} {
		w.table.SetCell(0, col, tview.NewTableCell(label).SetSelectable(false).SetAttributes(tcell.AttrBold).SetTextColor(palette.Focus.Color()))
	}
	selection := 1
	for i := range w.rows {
		entry := &w.rows[i]
		state, summary := w.entrySummary(entry)
		cells := []string{state, entry.Identity.Kind, entry.Identity.Namespace, entry.Identity.Name, summary}
		for col, value := range cells {
			cell := tview.NewTableCell(desiredReviewSafe(value)).SetTextColor(text)
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
			if col == 4 {
				cell.SetExpansion(1)
			}
			w.table.SetCell(i+1, col, cell)
		}
		if desiredReviewEntryKey(entry) == selected {
			selection = i + 1
		}
	}
	if len(w.rows) == 0 {
		message := "No rows match this local search. / changes or clears it."
		if w.query == "" {
			message = "Select a manifest with n. r refreshes live with the same source."
			if w.source.Identity.SHA256 != "" {
				message = "No resource review yet. r reads the retained source's live targets."
			}
		}
		w.table.Clear()
		w.table.SetCell(0, 0, tview.NewTableCell(message).SetSelectable(false).SetExpansion(1))
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
	return entry.State, summary
}
func (w *desiredReviewView) renderDetail() {
	if !w.detailOpen {
		return
	}
	entry, ok := w.selectedEntry()
	if !ok {
		w.detail.SetText("This retained detail is unavailable in the current report. Esc returns to the resource table.")
		return
	}
	text := w.renderSourceIdentity() + "\n\n" + renderDesiredReviewEntry(&entry)
	if reason, retained := w.retainedReasons[desiredReviewEntryKey(&entry)]; retained {
		text = desiredReviewSafe("RETAINED EVIDENCE · latest read "+reason+"\nOriginal observed time: "+identityTime(entry.ObservedAt)) + "\n\n" + text
	}
	w.detail.SetText(text)
}
func (w *desiredReviewView) renderHeader() {
	source := w.source.Identity
	sourceLine := "Source: not selected"
	if source.SHA256 != "" {
		fingerprint := source.SHA256[:min(12, len(source.SHA256))]
		sourceLine = fmt.Sprintf("Source: %s · SHA256 %s · %d documents", filepath.Base(source.Path), fingerprint, source.Documents)
	}
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
	changes, match, unknown := 0, 0, 0
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
		default:
			unknown++
		}
	}
	counts := fmt.Sprintf("%d resources · %d changed · %d reviewed matches · %d unavailable", len(w.snapshot.Entries), changes, match, unknown)
	if len(w.retainedReasons) > 0 {
		counts += fmt.Sprintf(" · %d retained", len(w.retainedReasons))
	}
	readTimes := "Loaded " + loaded + " · Read " + observed
	notice := w.notice
	if w.query != "" {
		notice = "Search: " + w.query + " · " + notice
	}
	w.header.SetText("[::b]" + desiredReviewSafe(destination) + "[::]\n" + desiredReviewSafe(sourceLine) + "\n" + desiredReviewSafe(counts) +
		"\n" + desiredReviewSafe(readTimes) + "\n" + desiredReviewSafe(notice))
}
func (w *desiredReviewView) renderSourceIdentity() string {
	source := w.source.Identity
	return desiredReviewSafe(fmt.Sprintf("RETAINED SOURCE\nPath: %s\nSHA256: %s\nLoaded: %s · %d bytes · %d documents\nLatest read attempt: %s\nAccepted report: %s",
		source.Path, source.SHA256, identityTime(source.LoadedAt), source.Bytes, source.Documents,
		identityTime(w.latestObservedAt), identityTime(w.snapshot.ObservedAt)))
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
	out.WriteString("\nUNREVIEWED\n")
	if len(entry.Intent.Unreviewed) == 0 {
		out.WriteString("API defaulting, admission, omitted live fields and pruning are outside this local comparison.\n")
	} else {
		for _, item := range entry.Intent.Unreviewed {
			out.WriteString("• " + item + "\n")
		}
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
func (w *desiredReviewView) Draw(screen tcell.Screen) {
	_, _, width, _ := w.GetInnerRect()
	if len(w.rows) == 0 {
		w.table.GetCell(0, 0).SetMaxWidth(max(1, width)).SetExpansion(1)
	} else {
		caps := []int{max(10, min(20, width/6)), max(8, min(18, width/8)), max(10, min(22, width/7)), max(14, min(30, width/5)), 0}
		remaining := width - 8
		for _, cap := range caps {
			remaining -= cap
		}
		caps[4] = max(8, remaining)
		for col, cap := range caps {
			for row := range w.table.GetRowCount() {
				w.table.GetCell(row, col).SetMaxWidth(cap)
			}
		}
	}
	w.Flex.Draw(screen)
}
