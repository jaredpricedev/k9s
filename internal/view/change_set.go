// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"errors"
	"strings"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/gitops"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/logstream"
	"github.com/derailed/k9s/internal/review"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const changeSetCommandToken = "changeset"
const changeSetTitle = "Reviewed change set"
const changeSetModalPage = "change-set-confirm"

var changeSetTabs = []string{"Plan", "Changes", "Ownership", "Outcomes", "Evidence"}

type changeSetView struct {
	*Details
	source                                  review.Source
	scope                                   review.Scope
	plan                                    *review.ChangeSetPlan
	session                                 *operationSession
	owner                                   review.OwnershipReader
	resolve                                 review.Resolver
	initial                                 []review.Entry
	preparer                                func(context.Context) (*review.ChangeSetPlan, error)
	table                                   *tview.Table
	pages                                   *tview.Pages
	identityBar, tabsBar, footer            *tview.TextView
	selected                                map[int]bool
	rows                                    []int
	activeTab, focusedEntry                 int
	tabStates                               [5]investigationTabState
	width, height                           int
	contextName, workspaceNamespace, notice string
	destinationRevision, generation         uint64
	active, loading                         bool
	cancel                                  context.CancelFunc
	enqueue                                 func(func())
	modal                                   *ui.ModalForm
	form                                    *tview.Form
	task                                    *operationTask
}

func (c *Command) changeSetCommand() {
	w, ok := c.app.Content.Top().(*desiredReviewView)
	if !ok {
		c.app.Flash().Warn("Open :" + desiredReviewCommandToken + " with a retained manifest or @source profile, then use b or :changeset")
		return
	}
	w.openChangeSet()
}

func (w *desiredReviewView) openChangeSet() {
	if !w.current() || w.source.Identity.SHA256 == "" {
		w.notice = "Choose a source in the current destination before opening a change set"
		w.render()
		return
	}
	connection, err := pinInspectionConnection(w.app.Conn())
	if err != nil {
		w.app.Flash().Err(err)
		return
	}
	dyn, dynamicErr := connection.DynDial()
	typed, typedErr := connection.Dial()
	if dynamicErr != nil || typedErr != nil || dyn == nil || typed == nil {
		w.app.Flash().Warn("Captured native operation/ownership clients unavailable")
		return
	}
	v := newChangeSetView(w.app, w.source, w.scope, w.snapshot.Entries)
	v.resolve = w.resolve
	v.owner = func(ctx context.Context, identity *inspect.ResourceIdentity) (*gitops.Snapshot, error) {
		reader := &gitopsReader{dynamic: dyn, discovery: typed.Discovery().RESTClient()}
		return gitops.Collect(ctx, reader, &gitops.Request{Target: *identity})
	}
	v.session = &operationSession{app: w.app, context: v.contextName, namespace: v.workspaceNamespace,
		revision: v.destinationRevision, dynamic: dyn, typed: typed, timeout: review.ChangeSetPlanLifetime,
		stillCurrent: func() bool { return v.active && v.destinationCurrent() && v.app.Content.Top() == v }}
	v.preparer = func(ctx context.Context) (*review.ChangeSetPlan, error) {
		return review.PrepareChangeSet(ctx, dyn, v.resolve, v.owner, v.source, v.scope)
	}
	if err := w.app.inject(v, false); err != nil {
		w.app.Flash().Err(err)
	}
}

//nolint:gocritic // The input source/scope are copied before this retained view owns them.
func newChangeSetView(app *App, source review.Source, scope review.Scope, initial []review.Entry) *changeSetView {
	source.Identity.Options = append([]string(nil), source.Identity.Options...)
	objects := make([]review.Manifest, len(source.Objects))
	for index := range source.Objects {
		objects[index] = source.Objects[index]
		if objects[index].Object != nil {
			objects[index].Object = (&unstructured.Unstructured{Object: source.Objects[index].Object}).DeepCopy().Object
		}
	}
	source.Objects = objects
	return &changeSetView{Details: NewDetails(app, changeSetTitle, source.Identity.Path, contentInspection, true),
		source: source, scope: copyDesiredReviewScope(scope), initial: append([]review.Entry(nil), initial...), selected: make(map[int]bool),
		contextName: scope.Context, workspaceNamespace: app.Config.ActiveNamespace(), destinationRevision: app.Config.DestinationRevision(), focusedEntry: 0}
}

func (*changeSetView) CompactWorkspace() bool { return true }
func (v *changeSetView) SelectedResource() SelectedResourceTarget {
	if v.plan != nil && v.focusedEntry >= 0 && v.focusedEntry < len(v.plan.Entries) {
		return changeSetTarget(&v.plan.Entries[v.focusedEntry].Identity)
	}
	if v.focusedEntry >= 0 && v.focusedEntry < len(v.initial) {
		return changeSetTarget(&v.initial[v.focusedEntry].Identity)
	}
	return SelectedResourceTarget{Context: v.contextName, UnavailableReason: "No captured resource identity"}
}

func (v *changeSetView) Init(ctx context.Context) error {
	if err := v.Details.Init(ctx); err != nil {
		return err
	}
	v.cmdBuff.RemoveListener(v.Details)
	v.cmdBuff.AddListener(v)
	v.SetBorder(false).SetBorderPadding(0, 0, 0, 0)
	v.app.Styles.RemoveListener(v.Details)
	v.app.Styles.AddListener(v)
	v.identityBar = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.tabsBar = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.footer = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.table = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0).SetEvaluateAllRows(false)
	v.table.SetSelectionChangedFunc(func(row, _ int) {
		if row > 0 && row <= len(v.rows) {
			v.focusedEntry = v.rows[row-1]
			v.renderChrome()
		}
	})
	v.table.SetInputCapture(v.tableKey)
	v.pages = tview.NewPages().AddPage("plan", v.table, true, true).AddPage("detail", v.text, true, false)
	v.Flex.Clear().SetDirection(tview.FlexRow).AddItem(v.identityBar, 3, 0, false).AddItem(v.tabsBar, 1, 0, false).
		AddItem(v.pages, 0, 1, true).AddItem(v.footer, 1, 0, false)
	v.bindChangeSetActions()
	v.render()
	return nil
}

func (v *changeSetView) Start() {
	v.active = true
	v.app.Styles.RemoveListener(v.Details)
	v.app.Styles.RemoveListener(v)
	v.app.Styles.AddListener(v)
	v.app.Prompt().SetModel(v.cmdBuff)
	v.StylesChanged(v.app.Styles)
}
func (v *changeSetView) Stop() {
	v.active = false
	v.stopPreparation()
	v.dismissForm()
	v.app.Styles.RemoveListener(v)
	v.Details.Stop()
}
func (v *changeSetView) StylesChanged(styles *config.Styles) {
	v.applyStyles(styles)
	if v.table != nil {
		p := styles.Semantic()
		v.table.SetBackgroundColor(p.Canvas.Color())
		v.table.SetSelectedStyle(tcell.StyleDefault.Background(p.Selected.Color()).Foreground(p.Text.Color()).Bold(true))
	}
	v.render()
}
func (v *changeSetView) Draw(screen tcell.Screen) {
	if ui.DrawTaskSizeNotice(screen, v.Flex.Box) {
		return
	}
	_, _, width, height := v.GetInnerRect()
	if v.width != width || v.height != height {
		v.width, v.height = width, height
		v.render()
	}
	if !v.destinationCurrent() && (v.loading || v.modal != nil) {
		if v.loading {
			v.stopPreparation()
		}
		v.notice = "Destination changed; retained plan cannot be submitted"
		if v.modal != nil {
			v.modal.SetText("Destination changed. This retained confirmation cannot execute. " +
				"Cancel or Esc returns to the captured plan; reopen review for the current destination.")
			if v.form != nil && v.form.GetButtonCount() > 1 {
				v.form.GetButton(1).SetLabel("Unavailable")
			}
		}
	}
	v.renderChrome()
	v.Flex.Draw(screen)
}
func (v *changeSetView) destinationCurrent() bool {
	return v.contextName == v.app.Config.ActiveContextName() && v.destinationRevision == v.app.Config.DestinationRevision() &&
		v.workspaceNamespace == v.app.Config.ActiveNamespace()
}
func (v *changeSetView) eligible() error {
	if !v.active || v.app.Content.Top() != v || !v.destinationCurrent() {
		return errors.New("Originating view or captured destination changed; reopen review")
	}
	if v.app.Config.IsReadOnly() {
		return errors.New("Change-set admission and execution are unavailable in read-only mode")
	}
	if v.session == nil || v.session.dynamic == nil || v.session.typed == nil {
		return errors.New("Captured native operation clients unavailable")
	}
	return nil
}
func (v *changeSetView) stopPreparation() {
	v.generation++
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	if v.loading {
		v.notice = "Preparation canceled; previous evidence retained, prepare again before execution"
	}
	v.loading = false
}
func (v *changeSetView) BufferCompleted(text, mode string) {
	v.Details.BufferCompleted(text, mode)
	if v.activeTab == 0 {
		v.renderRows()
	}
}
func changeSetSingleLine(value string) string {
	return strings.NewReplacer("\n", " ", "\t", " ").Replace(logstream.SafeText(value))
}
func changeSetTarget(identity *review.Identity) SelectedResourceTarget {
	raw := identity.GVR.Version + "/" + identity.GVR.Resource
	if identity.GVR.Group != "" {
		raw = identity.GVR.Group + "/" + raw
	}
	target := SelectedResourceTarget{Context: identity.Context, GVR: client.NewGVR(raw), Namespace: identity.Namespace, Name: identity.Name, UID: identity.UID}
	if identity.GVR.Resource == "" {
		target.UnavailableReason = "Exact API mapping has not been prepared"
	}
	return target
}
