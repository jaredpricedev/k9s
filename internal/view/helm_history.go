// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/k9s/internal/render/helm"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/ui/dialog"
	"github.com/derailed/tcell/v2"
)

// History represents a helm History view.
type History struct {
	ResourceViewer

	Values *model.RevValues
}

// NewHistory returns a new helm-history view.
func NewHistory(gvr *client.GVR) ResourceViewer {
	h := History{
		ResourceViewer: NewValueExtender(NewBrowser(gvr)),
	}
	h.GetTable().SetColorerFn(helm.History{}.ColorerFunc())
	h.GetTable().SetBorderFocusColor(tcell.ColorMediumSpringGreen)
	h.GetTable().SetSelectedStyle(tcell.StyleDefault.Foreground(tcell.ColorWhite).Background(tcell.ColorMediumSpringGreen).Attributes(tcell.AttrNone))
	h.AddBindKeysFn(h.bindKeys)
	h.SetContextFn(h.HistoryContext)
	h.GetTable().SetEnterFn(h.getValsCmd)

	return &h
}

// Init initializes the view
func (h *History) Init(ctx context.Context) error {
	if err := h.ResourceViewer.Init(ctx); err != nil {
		return err
	}
	h.GetTable().SetSortCol("REVISION", false)

	return nil
}

func (*History) HistoryContext(ctx context.Context) context.Context {
	return ctx
}

func (h *History) bindKeys(aa *ui.KeyActions) {
	if !h.App().Config.IsReadOnly() {
		h.bindDangerousKeys(aa)
	}

	aa.Delete(ui.KeyShiftA, ui.KeyShiftN, tcell.KeyCtrlS, tcell.KeyCtrlSpace, ui.KeySpace, tcell.KeyCtrlD)
	aa.Bulk(ui.KeyMap{
		ui.KeyShiftN: ui.NewKeyAction("Sort Revision", h.GetTable().SortColCmd("REVISION", true), false),
		ui.KeyShiftA: ui.NewKeyAction("Sort Age", h.GetTable().SortColCmd("AGE", true), false),
	})
}

func (h *History) getValsCmd(app *App, _ ui.Tabular, _ *client.GVR, path string) {
	ns, n := client.Namespaced(path)
	tt := strings.Split(n, ":")
	if len(tt) < 2 {
		app.Flash().Err(fmt.Errorf("unable to parse version in %q", path))
		return
	}
	name, rev := tt[0], tt[1]
	h.Values = model.NewRevValues(h.GVR(), client.FQN(ns, name), rev)
	v := NewLiveView(h.App(), "Values", h.Values)
	if err := v.app.inject(v, false); err != nil {
		v.app.Flash().Err(err)
	}
}

func (h *History) bindDangerousKeys(aa *ui.KeyActions) {
	aa.Add(ui.KeyR, ui.NewKeyActionWithOpts("RollBackTo...", h.rollbackCmd,
		ui.ActionOpts{
			Visible:   true,
			Dangerous: true,
		},
	))
}

func (h *History) rollbackCmd(evt *tcell.EventKey) *tcell.EventKey {
	path := h.GetTable().GetSelectedItem()
	if path == "" {
		return evt
	}

	ns, nrev := client.Namespaced(path)
	tt := strings.Split(nrev, ":")
	if len(tt) != 2 {
		h.App().Flash().Warn("Select an explicit Helm revision before rollback")
		return nil
	}
	n, rev := tt[0], tt[1]
	version, err := strconv.Atoi(rev)
	if err != nil || version <= 0 {
		h.App().Flash().Warn("Select an explicit positive Helm revision")
		return nil
	}
	session, err := captureOperationScreen(h)
	if err != nil {
		h.App().Flash().Err(err)
		return nil
	}
	factory, err := captureSyntheticOperationFactory(h, session.context)
	if err != nil || factory.Client() == nil || factory.Client().Config() == nil {
		h.App().Flash().Warn("Helm operation client is unavailable")
		return nil
	}
	row, found := h.GetTable().GetModel().Peek().FindRow(path)
	if !found {
		h.App().Flash().Warn("Selected Helm revision is no longer retained; refresh first")
		return nil
	}
	expected := slices.Clone(row.Row.Fields)
	var hm dao.HelmHistory
	hm.Init(factory, h.GVR())
	h.App().Flash().Info("Reading selected Helm revision before confirmation...")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), boundedOperationTimeout(session.timeout))
		defer cancel()
		if destinationErr := freezeHelmOperationDestination(factory); destinationErr != nil {
			session.dispatch(func() { h.App().Flash().Err(destinationErr) })
			return
		}
		obj, err := hm.Get(ctx, path)
		var fingerprint string
		if err == nil {
			revision, ok := obj.(helm.ReleaseRes)
			if !ok || revision.Release == nil || revision.Release.Info == nil || revision.Release.Chart == nil || revision.Release.Chart.Metadata == nil {
				err = fmt.Errorf("selected Helm revision metadata is unavailable; refresh and review again")
			} else {
				current := model1.NewRow(len(expected))
				err = (helm.History{}).Render(obj, ns, &current)
				if err == nil && !slices.Equal(current.Fields, expected) {
					err = fmt.Errorf("selected Helm revision changed; refresh and review again")
				}
				if err == nil {
					fingerprint, err = dao.HelmRevisionFingerprint(revision.Release)
				}
			}
		}
		session.dispatch(func() {
			if err != nil {
				h.App().Flash().Err(err)
				return
			}
			msg := fmt.Sprintf("Rollback Helm release %s/%s to revision %s?\nContext: %s\nProvider identity: %s\nHelm retains "+
				"ownership. Cancellation does not undo accepted writes.", ns, n, rev, session.context, fingerprint)
			dialog.ShowConfirmAck(h.App().App, h.App().Content.Pages, n, false, "Confirm Rollback", msg, func() {
				target := SelectedResourceTarget{Context: session.context, GVR: h.GVR(), Namespace: ns, Name: n + ":" + rev}
				session.submit("Helm rollback to "+rev, []SelectedResourceTarget{target}, func(ctx context.Context, _ SelectedResourceTarget) error {
					fmt.Fprintf(operationOutput(ctx), "Provider identity: %s\n", fingerprint)
					ctx = observeHelmOperation(ctx)
					return hm.RollbackSnapshot(ctx, client.FQN(ns, n), rev, fingerprint)
				}, nil)
			}, func() {})
		})
	}()
	return nil
}

func observeHelmOperation(ctx context.Context) context.Context {
	return dao.WithHelmOperationObserver(ctx, func(method, resourcePath string, accepted bool) {
		if accepted {
			operationAcceptWrite(ctx, method+" "+resourcePath)
		} else {
			operationBeginWrite(ctx)
		}
	})
}
