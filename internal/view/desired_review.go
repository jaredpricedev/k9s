// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/provider"
	"github.com/derailed/k9s/internal/review"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/view/cmd"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

const (
	desiredReviewTitle        = "Desired-state review"
	desiredReviewSourcePage   = "desired-review-source"
	desiredReviewTablePage    = "resources"
	desiredReviewDetailPage   = "detail"
	desiredReviewCommandToken = "review"
	desiredReviewSecretKind   = "Secret"
	desiredReviewSecrets      = "secrets"
)

// desiredReviewView retains authored input separately from display-safe reports.
// All presentation belongs to the draw goroutine. The gate prevents a completed
// worker from submitting an application update after the view has stopped.
type desiredReviewView struct {
	*tview.Flex
	app                             *App
	table                           *tview.Table
	header, footer, detail          *tview.TextView
	pages                           *tview.Pages
	prompt                          *tview.InputField
	actions                         *ui.KeyActions
	scope                           review.Scope
	source                          review.Source
	snapshot                        review.Snapshot
	latest                          []review.Entry
	rows                            []review.Entry
	retainedReasons                 map[string]string
	latestObservedAt                time.Time
	reader                          dynamic.Interface
	resolve                         review.Resolver
	load                            func(context.Context, string) (review.Source, error)
	loadProfile                     func(context.Context, string, provider.Scope, review.ProviderRun) (review.Source, error)
	runProvider                     review.ProviderRun
	preview                         func(context.Context, dynamic.Interface, review.Resolver, review.Source, review.Scope, time.Time) review.ServerPreview
	serverPreview                   *review.ServerPreview
	serverPreviewEvidence           string
	previewOpen                     bool
	collect                         func(context.Context, dynamic.Interface, review.Resolver, review.Source, review.Scope, time.Time) review.Snapshot
	path, query, notice             string
	contextName                     string
	revision, generation            uint64
	cancel                          context.CancelFunc
	prompting, formOpen, detailOpen bool
	detailKey                       string
	evidenceOpen                    bool
	width, height                   int
	originalCapture                 func(*tcell.EventKey) *tcell.EventKey
	gate                            sync.Mutex
	active                          bool
	// enqueue must be nonblocking, as is the ui.App queue wrapper.
	enqueue func(func())
}

var _ model.Component = (*desiredReviewView)(nil)
var _ SelectedResource = (*desiredReviewView)(nil)

func (c *Command) desiredReviewCommand(line string) {
	line = strings.TrimPrefix(strings.TrimSpace(line), ":")
	path := strings.TrimSpace(strings.TrimPrefix(line, desiredReviewCommandToken))
	c.app.openDesiredReview(path)
}
func (a *App) openDesiredReview(path string) {
	scope, err := captureReviewScope(a)
	if err != nil {
		a.Flash().Err(err)
		return
	}
	connection, err := pinInspectionConnection(a.Conn())
	if err != nil {
		a.Flash().Err(err)
		return
	}
	reader, err := connection.DynDial()
	if err != nil {
		a.Flash().Err(err)
		return
	}
	w := newDesiredReviewView(a, scope, path)
	w.reader = reader
	w.resolve = reviewResolver(connection)
	if err := a.inject(w, false); err != nil {
		a.Flash().Err(err)
	}
}

//nolint:gocritic // The view captures a scope value before asynchronous reads.
func newDesiredReviewView(a *App, scope review.Scope, path string) *desiredReviewView {
	w := &desiredReviewView{
		Flex: tview.NewFlex().SetDirection(tview.FlexRow), app: a, table: tview.NewTable(),
		header: tview.NewTextView(), footer: tview.NewTextView(), detail: tview.NewTextView(),
		pages: tview.NewPages(), prompt: tview.NewInputField(),
		scope: copyDesiredReviewScope(scope), path: path, contextName: scope.Context,
		load: review.LoadSource, loadProfile: review.LoadSourceProfile, preview: review.PreviewServer, collect: review.Collect,
	}
	if a != nil && a.Config != nil {
		w.revision = a.Config.DestinationRevision()
	}
	if a != nil {
		w.enqueue = a.QueueUpdateDraw
	}
	w.table.SetSelectable(true, false).SetFixed(1, 0).SetEvaluateAllRows(false)
	for _, v := range []*tview.TextView{w.header, w.footer, w.detail} {
		v.SetDynamicColors(true)
	}
	w.header.SetWrap(false)
	w.footer.SetWrap(false)
	w.detail.SetWrap(true).SetScrollable(true)
	w.pages.AddPage(desiredReviewTablePage, w.table, true, true).AddPage(desiredReviewDetailPage, w.detail, true, false)
	w.AddItem(w.header, 5, 0, false).AddItem(w.pages, 0, 1, true).AddItem(w.footer, 1, 0, false)
	w.SetBorder(true).SetTitle(" " + desiredReviewTitle + " ")
	w.table.SetInputCapture(w.key)
	w.detail.SetInputCapture(w.key)
	w.prompt.SetLabel("Search kind / namespace / name / state: ")
	w.prompt.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEnter {
			w.query = strings.TrimSpace(w.prompt.GetText())
			w.render()
		}
		if key == tcell.KeyEnter || key == tcell.KeyEscape {
			w.prompting = false
			w.RemoveItem(w.prompt)
			w.focusContent()
		}
	})
	w.actions = w.makeActions()
	return w
}
func (*desiredReviewView) Name() string                           { return desiredReviewTitle }
func (*desiredReviewView) CompactWorkspace() bool                 { return true }
func (*desiredReviewView) SetCommand(*cmd.Interpreter)            {}
func (*desiredReviewView) SetLabelSelector(labels.Selector, bool) {}
func (w *desiredReviewView) SetFilter(query string, _ bool) {
	w.query = strings.TrimSpace(query)
	w.render()
}
func (w *desiredReviewView) InCmdMode() bool         { return w.prompting || w.formOpen }
func (w *desiredReviewView) Actions() *ui.KeyActions { return w.actions }
func (w *desiredReviewView) Hints() model.MenuHints  { return actionCatalogHints(w, w.app) }
func (*desiredReviewView) ExtraHints() map[string]string {
	return map[string]string{
		"Review": "Local declared-field comparison; r refreshes live with the same retained source. " +
			"Enter opens changes; e opens provenance. p explicitly confirms server admission preview.",
		"Source": "n selects/reloads a manifest or @source-profile.yaml. Configured renderer/Git commands run only on that explicit source selection."}
}
func (w *desiredReviewView) Init(context.Context) error {
	w.StylesChanged(w.app.Styles)
	w.render()
	return nil
}
func (w *desiredReviewView) Start() {
	w.gate.Lock()
	if w.active {
		w.gate.Unlock()
		return
	}
	w.active = true
	w.gate.Unlock()
	w.app.Styles.RemoveListener(w)
	w.app.Styles.AddListener(w)
	w.StylesChanged(w.app.Styles)
	w.originalCapture = w.app.GetInputCapture()
	w.app.SetInputCapture(func(e *tcell.EventKey) *tcell.EventKey {
		if w.prompting || w.formOpen {
			return e
		}
		if !w.app.Prompt().InCmdMode() && (e.Rune() == '/' || e.Key() == tcell.KeyEscape) {
			return w.key(e)
		}
		if w.originalCapture != nil {
			return w.originalCapture(e)
		}
		return e
	})
	if w.source.Identity.SHA256 == "" {
		if w.path == "" {
			w.sourceForm()
		} else {
			w.loadSource(w.path)
		}
	} else {
		w.render()
	}
}
func (w *desiredReviewView) Stop() {
	w.gate.Lock()
	wasActive := w.active
	w.active = false
	w.gate.Unlock()
	w.generation++
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
		if strings.HasPrefix(w.notice, "Reading") || strings.HasPrefix(w.notice, "Loading") {
			w.notice = "Read canceled while away; retained evidence. r refreshes live state."
		}
	}
	if w.formOpen {
		w.app.Content.Pages.ClearPageResources()
		w.app.Content.Pages.RemovePage(desiredReviewSourcePage)
		w.formOpen = false
	}
	if wasActive {
		w.app.Styles.RemoveListener(w)
		w.app.SetInputCapture(w.originalCapture)
	}
}
func (w *desiredReviewView) StylesChanged(styles *config.Styles) {
	p := styles.Semantic()
	canvas := p.Canvas.Color()
	text := config.ReadableForeground(p.Text.Color(), canvas)
	w.SetBackgroundColor(canvas).SetBorderColor(p.Muted.Color()).SetBorderFocusColor(p.Focus.Color()).SetTitleColor(p.Focus.Color())
	w.table.SetBackgroundColor(canvas)
	w.table.SetSelectedStyle(tcell.StyleDefault.Background(p.Selected.Color()).Foreground(config.ReadableForeground(text, p.Selected.Color())).Bold(true))
	for _, v := range []*tview.TextView{w.header, w.footer, w.detail} {
		v.SetBackgroundColor(canvas)
		v.SetTextColor(text)
	}
	w.prompt.SetFieldBackgroundColor(p.Panel.Color()).SetFieldTextColor(text).SetLabelColor(p.Focus.Color())
	w.render()
}
func (w *desiredReviewView) makeActions() *ui.KeyActions {
	actions := ui.NewKeyActions()
	for _, item := range []struct {
		key       tcell.Key
		id, label string
		selected  bool
	}{
		{tcell.KeyEnter, "review.detail", "Review detail", false},
		{ui.KeyE, "review.evidence", "Evidence / source", false},
		{ui.KeyP, "review.server-preview", "Explicit server dry-run / admission preview", false},
		{ui.KeyB, "review.change-set", "Open reviewed change set", false},
		{ui.KeyR, "review.refresh", "Refresh live", false},
		{ui.KeyN, "review.source", "Select / reload source", false},
		{ui.KeySlash, "review.search", dailyWorkspaceSearchLabel, false},
		{ui.KeyI, "review.inspect", "Investigate live resource", true},
		{tcell.KeyEscape, "review.back", dailyWorkspaceBackLabel, false},
	} {
		key := item.key
		action := ui.NewKeyActionWithOpts(item.label, func(*tcell.EventKey) *tcell.EventKey { return w.key(actionEvent(key)) },
			ui.ActionOpts{Visible: true, RequiresSelection: item.selected})
		action.ID = item.id
		action.Category = ui.ActionInspect
		if item.selected {
			action.Availability = func() string {
				target := w.SelectedResource()
				if err := target.Err(); err != nil {
					return err.Error()
				}
				if target.Context != w.app.Config.ActiveContextName() {
					return "Context changed; reopen review"
				}
				return ""
			}
		}
		actions.Add(key, action)
	}
	actions.Add(tcell.KeyCtrlO, ui.NewKeyAction("Actions", w.app.actionsCmd, true))
	return actions
}
func (w *desiredReviewView) key(e *tcell.EventKey) *tcell.EventKey {
	if w.prompting || w.formOpen {
		return e
	}
	switch {
	case e.Key() == tcell.KeyEscape:
		if w.detailOpen {
			w.detailOpen = false
			w.evidenceOpen = false
			w.previewOpen = false
			w.pages.SwitchToPage(desiredReviewTablePage)
			w.render()
			w.focusContent()
			return nil
		}
		return w.app.PrevCmd(e)
	case e.Key() == tcell.KeyEnter:
		if entry, ok := w.selectedEntry(); ok {
			if !w.detailOpen || w.detailKey != desiredReviewEntryKey(&entry) {
				w.detail.ScrollToBeginning()
			}
			w.detailOpen = true
			w.evidenceOpen = false
			w.previewOpen = false
			w.detailKey = desiredReviewEntryKey(&entry)
			w.pages.SwitchToPage(desiredReviewDetailPage)
			w.render()
			w.focusContent()
		}
		return nil
	case e.Rune() == 'e':
		if entry, ok := w.selectedEntry(); ok {
			w.detailKey = desiredReviewEntryKey(&entry)
		}
		w.evidenceOpen = !w.evidenceOpen
		w.detailOpen = true
		w.detail.ScrollToBeginning()
		w.pages.SwitchToPage(desiredReviewDetailPage)
		w.render()
		w.focusContent()
		return nil
	case e.Rune() == 'p':
		w.confirmServerPreview()
		return nil
	case e.Rune() == 'b':
		w.openChangeSet()
		return nil
	case e.Rune() == 'r':
		w.refresh()
		return nil
	case e.Rune() == 'n':
		w.sourceForm()
		return nil
	case e.Rune() == '/':
		w.prompting = true
		w.prompt.SetText(w.query)
		w.AddItem(w.prompt, 1, 0, true)
		w.app.SetFocus(w.prompt)
		return nil
	case e.Rune() == 'i':
		w.app.openTargetInspection(w.SelectedResource(), troubleshootCommand)
		return nil
	}
	return e
}
func (w *desiredReviewView) focusContent() {
	if w.detailOpen {
		w.app.SetFocus(w.detail)
	} else {
		w.app.SetFocus(w.table)
	}
}
func (w *desiredReviewView) selectedEntry() (review.Entry, bool) {
	if w.detailOpen {
		for i := range w.snapshot.Entries {
			entry := &w.snapshot.Entries[i]
			if desiredReviewEntryKey(entry) == w.detailKey {
				return *entry, true
			}
		}
		return review.Entry{}, false
	}
	row, _ := w.table.GetSelection()
	if row < 1 || row > len(w.rows) {
		return review.Entry{}, false
	}
	return w.rows[row-1], true
}
func (w *desiredReviewView) SelectedResource() SelectedResourceTarget {
	entry, ok := w.selectedEntry()
	if !ok {
		return SelectedResourceTarget{Context: w.contextName, UnavailableReason: "Select an observed live resource first"}
	}
	identity := entry.Identity
	if strings.EqualFold(identity.Kind, desiredReviewSecretKind) || identity.GVR.Resource == desiredReviewSecrets {
		return SelectedResourceTarget{Context: w.contextName, UnavailableReason: "Secret values and live identity are excluded from this review"}
	}
	for i := range w.latest {
		latest := &w.latest[i]
		if desiredReviewEntryKey(latest) == desiredReviewEntryKey(&entry) && latest.State == review.StateStale {
			return SelectedResourceTarget{Context: w.contextName, UnavailableReason: "Live identity changed; reopen review to choose the current resource"}
		}
	}
	if (entry.State != review.StateChanged && entry.State != review.StateMatch) || identity.UID == "" || identity.GVR.Resource == "" {
		return SelectedResourceTarget{Context: w.contextName, UnavailableReason: "This review row has no readable, identity-verified live resource"}
	}
	raw := identity.GVR.Version + "/" + identity.GVR.Resource
	if identity.GVR.Group != "" {
		raw = identity.GVR.Group + "/" + raw
	}
	return SelectedResourceTarget{Context: identity.Context, GVR: client.NewGVR(raw), Namespace: identity.Namespace, Name: identity.Name, UID: identity.UID}
}
func desiredReviewAccepts(contextName, currentContext string, revision, currentRevision uint64, top bool) bool {
	return contextName == currentContext && revision == currentRevision && top
}
func (w *desiredReviewView) current() bool {
	return desiredReviewAccepts(w.contextName, w.app.Config.ActiveContextName(), w.revision, w.app.Config.DestinationRevision(), w.app.Content.Top() == w)
}
func (w *desiredReviewView) beginRead() context.Context {
	if w.cancel != nil {
		w.cancel()
	}
	w.generation++
	ctx, cancel := context.WithTimeout(context.Background(), review.CollectionTimeout)
	w.cancel = cancel
	return ctx
}
func (w *desiredReviewView) submit(ctx context.Context, update func()) bool {
	w.gate.Lock()
	defer w.gate.Unlock()
	if !w.active || ctx.Err() == context.Canceled || w.enqueue == nil {
		return false
	}
	if w.app != nil && !w.app.IsRunning() {
		return false
	}
	w.enqueue(update)
	return true
}
func (w *desiredReviewView) loadSource(path string) {
	if !w.current() {
		w.notice = "Destination changed; reopen review before choosing a source."
		w.render()
		return
	}
	ctx := w.beginRead()
	generation := w.generation
	load := w.load
	profileLoader, runner := w.loadProfile, w.runProvider
	scope := provider.Scope{Context: w.contextName, Namespace: w.scope.DefaultNamespace, Revision: w.revision}
	cancel := w.cancel
	w.notice = "Loading explicitly selected source…"
	w.render()
	go func() {
		defer cancel()
		var source review.Source
		var err error
		if strings.HasPrefix(path, "@") {
			source, err = profileLoader(ctx, strings.TrimPrefix(path, "@"), scope, runner)
		} else {
			source, err = load(ctx, path)
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		w.submit(ctx, func() {
			if generation != w.generation || !w.current() {
				return
			}
			w.cancel = nil
			if err != nil {
				w.notice = "Source load failed; prior source and evidence retained: " + err.Error()
				w.render()
				return
			}
			w.source = source
			w.path = source.Identity.Path
			if source.Identity.Provider != "" {
				w.path = "@" + source.Identity.Path
			}
			w.snapshot = review.Snapshot{}
			w.latest = nil
			w.retainedReasons = nil
			w.latestObservedAt = time.Time{}
			w.rows = nil
			w.detailOpen = false
			w.evidenceOpen = false
			w.previewOpen = false
			w.serverPreview = nil
			w.serverPreviewEvidence = ""
			w.detailKey = ""
			w.pages.SwitchToPage(desiredReviewTablePage)
			w.refresh()
		})
	}()
}
func (w *desiredReviewView) refresh() {
	if w.source.Identity.SHA256 == "" {
		w.notice = "Select a local manifest with n before reviewing live state."
		w.render()
		return
	}
	if !w.current() {
		w.notice = "Destination changed; reopen review. Retained evidence belongs to its original context."
		w.render()
		return
	}
	if w.reader == nil {
		w.notice = "Live reader unavailable; retained evidence."
		w.render()
		return
	}
	ctx := w.beginRead()
	generation := w.generation
	cancel := w.cancel
	source, scope, reader, resolve, collect := w.source, copyDesiredReviewScope(w.scope), w.reader, w.resolve, w.collect
	w.notice = "Reading live resources… source stays fixed"
	w.render()
	go func() {
		defer cancel()
		snapshot := collect(ctx, reader, resolve, source, scope, time.Now())
		err := ctx.Err()
		w.submit(ctx, func() {
			if generation != w.generation || !w.current() {
				return
			}
			w.cancel = nil
			w.acceptSnapshot(snapshot, err)
			w.render()
		})
	}()
}

//nolint:gocritic // Capture the immutable scope before asynchronous readers use it.
func copyDesiredReviewScope(scope review.Scope) review.Scope {
	scope.Namespaces = append([]string(nil), scope.Namespaces...)
	scope.Kinds = append([]string(nil), scope.Kinds...)
	uids := make(map[string]types.UID, len(scope.CapturedUIDs))
	for key, uid := range scope.CapturedUIDs {
		uids[key] = uid
	}
	scope.CapturedUIDs = uids
	return scope
}

//nolint:gocritic // Accept a display-safe report value, never its raw authored objects.
func (w *desiredReviewView) acceptSnapshot(snapshot review.Snapshot, err error) {
	if snapshot.Source.SHA256 != w.source.Identity.SHA256 || snapshot.Source.Path != w.source.Identity.Path {
		w.notice = "Source identity differed; prior evidence retained."
		return
	}
	w.latest = append([]review.Entry(nil), snapshot.Entries...)
	w.latestObservedAt = snapshot.ObservedAt
	reasons := make(map[string]string)
	successful, unavailable := 0, 0
	for i := range snapshot.Entries {
		entry := &snapshot.Entries[i]
		switch entry.State {
		case review.StateChanged, review.StateMatch, review.StateCreate:
			successful++
		case review.StateDenied, review.StateUnknown, review.StateStale:
			unavailable++
			reasons[desiredReviewEntryKey(entry)] = entry.State + ": " + entry.Reason
		}
	}
	if (err != nil || (successful == 0 && unavailable > 0)) && !w.snapshot.ObservedAt.IsZero() {
		for i := range w.snapshot.Entries {
			entry := &w.snapshot.Entries[i]
			key := desiredReviewEntryKey(entry)
			if _, failed := reasons[key]; !failed && err != nil {
				reasons[key] = err.Error()
			}
		}
		w.retainedReasons = reasons
		w.notice = fmt.Sprintf("Refresh unavailable (%d reads); retained evidence from %s.", unavailable, w.snapshot.ObservedAt.UTC().Format(time.RFC3339))
		if err != nil {
			w.notice = "Refresh failed; retained evidence: " + err.Error()
		}
		return
	}
	// Failed rows retain their prior values and timestamps independently of the
	// fresh rows. The latest read states remain separate coverage evidence.
	prior := make(map[string]review.Entry, len(w.snapshot.Entries))
	for i := range w.snapshot.Entries {
		entry := &w.snapshot.Entries[i]
		prior[desiredReviewEntryKey(entry)] = *entry
	}
	snapshot.Entries = append([]review.Entry(nil), snapshot.Entries...)
	retained := make(map[string]string)
	for i := range snapshot.Entries {
		entry := &snapshot.Entries[i]
		key := desiredReviewEntryKey(entry)
		failure, failed := reasons[key]
		old, hasOld := prior[key]
		if failed && hasOld && (old.State == review.StateChanged || old.State == review.StateMatch || old.State == review.StateCreate) {
			snapshot.Entries[i] = old
			retained[key] = failure
		}
	}
	w.snapshot = snapshot
	w.retainedReasons = retained
	w.captureAcceptedIdentities()
	w.notice = "Local declared-field preview. No execution or pruning plan. r refreshes live only."
	if err != nil {
		w.notice = "Partial read: " + err.Error()
	} else if unavailable > 0 {
		w.notice = fmt.Sprintf("Partial review: %d unavailable reads. %d retained rows keep their original observation times.", unavailable, len(retained))
	}
}
func (w *desiredReviewView) captureAcceptedIdentities() {
	for i := range w.snapshot.Entries {
		entry := &w.snapshot.Entries[i]
		if entry.Identity.UID == "" || (entry.State != review.StateChanged && entry.State != review.StateMatch) {
			continue
		}
		key := review.IdentityKey(entry.Identity.GVR, entry.Identity.Namespace, entry.Identity.Name)
		if _, captured := w.scope.CapturedUIDs[key]; !captured {
			w.scope.CapturedUIDs[key] = entry.Identity.UID
		}
	}
}
func (w *desiredReviewView) sourceForm() {
	w.formOpen = true
	path := w.path
	styles := w.app.Styles.Dialog()
	form := tview.NewForm().SetItemPadding(0).SetButtonsAlign(tview.AlignCenter)
	form.SetBorderPadding(0, 0, 0, 0)
	form.SetButtonBackgroundColor(styles.ButtonBgColor.Color()).SetButtonTextColor(styles.ButtonFgColor.Color()).SetLabelColor(styles.LabelFgColor.Color()).
		SetFieldTextColor(styles.FieldFgColor.Color()).SetFieldBackgroundColor(styles.BgColor.Color())
	dismiss := func() { w.formOpen = false; w.app.Content.Pages.RemovePage(desiredReviewSourcePage); w.focusContent() }
	form.AddInputField("Local manifest file", path, 56, nil, func(value string) { path = value })
	form.AddButton("Cancel", dismiss).AddButton("Load source and review", func() {
		path = strings.TrimSpace(path)
		if path == "" {
			w.app.Flash().Warn("Choose a local manifest file")
			return
		}
		dismiss()
		w.loadSource(path)
	})
	form.SetCancelFunc(dismiss)
	frame := tview.NewFrame(form).SetBorders(0, 0, 1, 0, 0, 0)
	frame.SetBorder(true).SetBorderPadding(1, 1, 1, 1).SetTitle(" Select review source ").SetBackgroundColor(styles.BgColor.Color())
	modal := &dailyWorkspaceModal{Frame: frame, form: form, color: styles.FgColor.Color(),
		message: "Select a YAML/JSON manifest, or @/path/source-profile.yaml for a configured renderer or Git revision. Reloading observes a new source; r keeps it fixed. " +
			"Live reads stay inside the captured destination and workspace scope."}
	w.app.Content.Pages.AddPage(desiredReviewSourcePage, modal, false, true)
	w.app.SetFocus(modal)
}
