// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package view

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/flux"
	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/ui/dialog"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/version"
)

// Flux presents the unified Flux browser and native per-kind Flux operations.
type Flux struct{ ResourceViewer }

func NewFlux(gvr *client.GVR) ResourceViewer {
	f := &Flux{ResourceViewer: NewBrowser(gvr)}
	f.GetTable().SetLiteralFields(true)
	f.AddBindKeysFn(f.bindKeys)
	if gvr == client.FluxGVR {
		f.GetTable().SetEnterFn(f.openResource)
		f.GetTable().SetSortCol("STATUS", true)
		f.SetEnvFn(f.selectedEnv)
	}
	return f
}

func (f *Flux) selectedEnv() Env {
	gvr, fqn, ok := parseFluxPath(f.GetTable().GetSelectedItem())
	if !ok {
		return Env{}
	}
	row := f.GetTable().GetSelectedRow(f.GetTable().GetSelectedItem())
	env := defaultEnv(f.App().Conn().Config(), fqn, f.GetTable().GetModel().Peek().Header(), row)
	env["RESOURCE_GROUP"], env["RESOURCE_VERSION"], env["RESOURCE_NAME"] = gvr.G(), gvr.V(), gvr.R()
	return env
}

func (f *Flux) bindKeys(aa *ui.KeyActions) {
	aa.Add(ui.KeyI, ui.NewKeyAction("Status Details", f.statusCmd, true))
	aa.Add(ui.KeyShiftK, ui.NewKeyAction("Sort Kind", f.GetTable().SortColCmd("KIND", true), false))
	if f.GVR() == client.FluxGVR {
		aa.Delete(ui.KeyN, ui.KeyW)
		return // Open a concrete resource before invoking resource actions.
	}
	aa.Add(ui.KeyG, ui.NewKeyAction("Source/Dependencies", f.relatedCmd, true))
	if f.App().Config.IsReadOnly() || !dao.FluxNativeActions(f.GVR()) {
		return
	}
	aa.Add(ui.KeyShiftR, ui.NewKeyActionWithOpts("Reconcile", f.reconcileCmd, ui.ActionOpts{Visible: true, Dangerous: true}))
	aa.Add(ui.KeyShiftT, ui.NewKeyActionWithOpts("Suspend/Resume", f.suspendCmd, ui.ActionOpts{Visible: true, Dangerous: true}))
}

// statusCmd shows the complete selected summary, including wide-only MESSAGE.
// Reading the existing row also supports synthetic restricted/unavailable rows.
func (f *Flux) statusCmd(evt *tcell.EventKey) *tcell.EventKey {
	table := f.GetTable()
	path := table.GetSelectedItem()
	row := table.GetSelectedRow(path)
	if row == nil {
		return evt
	}
	details := NewDetails(f.App(), "Flux Status", path, contentTXT, false).
		Update(fluxStatusDetails(table.GetModel().Peek().Header(), row))
	if err := f.App().inject(details, false); err != nil {
		f.App().Flash().Err(err)
	}
	return nil
}

func fluxStatusDetails(header model1.Header, row *model1.Row) string {
	if row == nil {
		return ""
	}
	var text strings.Builder
	for _, name := range []string{"NAMESPACE", "NAME", "KIND", "STATUS", "SUSPEND", "REVISION", "SOURCE", "MESSAGE"} {
		index, ok := header.IndexOf(name, true)
		if !ok || index >= len(row.Fields) {
			continue
		}
		fmt.Fprintf(&text, "%s: %s\n", name, row.Fields[index])
	}
	return tview.Escape(text.String())
}

func parseFluxPath(path string) (*client.GVR, string, bool) {
	resource, fqn, ok := strings.Cut(path, "|")
	if !ok || strings.ContainsAny(fqn, "<>|") {
		return client.NoGVR, "", false
	}
	gvr := client.NewGVR(resource)
	ns, name := client.Namespaced(fqn)
	return gvr, fqn, flux.Supported(gvr) && ns != "" && name != ""
}

func (*Flux) openResource(app *App, _ ui.Tabular, _ *client.GVR, path string) {
	gvr, fqn, ok := parseFluxPath(path)
	if !ok {
		app.Flash().Warn("This resource kind is unavailable; see its message column")
		return
	}
	ns, _ := client.Namespaced(fqn)
	app.gotoResource(gvr.String()+" "+ns, fqn, false, true)
}

func (f *Flux) selected() (*unstructured.Unstructured, string, error) {
	fqn := f.GetTable().GetSelectedItem()
	if fqn == "" {
		return nil, "", fmt.Errorf("select a Flux resource")
	}
	o, err := f.App().factory.Get(f.GVR(), fqn, true, labels.Everything())
	if err != nil {
		return nil, "", err
	}
	u, ok := o.(*unstructured.Unstructured)
	if !ok {
		return nil, "", fmt.Errorf("expected a Flux resource, got %T", o)
	}
	return u, fqn, nil
}

func (f *Flux) reconcileCmd(evt *tcell.EventKey) *tcell.EventKey {
	return f.confirmAction(evt, "reconcile")
}

func (f *Flux) suspendCmd(evt *tcell.EventKey) *tcell.EventKey {
	return f.confirmAction(evt, "toggle")
}

func (f *Flux) confirmAction(evt *tcell.EventKey, action string) *tcell.EventKey {
	app := f.App()
	if app.Config.IsReadOnly() {
		return nil
	}
	o, fqn, err := f.selected()
	if err != nil {
		app.Flash().Err(err)
		return evt
	}
	if action == "toggle" {
		action = "suspend"
		if flux.Suspended(o) {
			action = "resume"
		}
	}
	if action == "reconcile" && flux.Suspended(o) {
		app.Flash().Warn("Resume this resource before reconciling it")
		return nil
	}
	operation, err := dao.PrepareFluxAction(app.Conn(), f.GVR(), fqn, o.GetUID(), action)
	if err != nil {
		app.Flash().Err(err)
		return nil
	}
	contextName := app.Config.ActiveContextName()
	timeout := app.Conn().Config().CallTimeout()
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	message := fmt.Sprintf("%s %s %s in context %s?", action, o.GetKind(), fqn, contextName)
	d := app.Styles.Dialog()
	dialog.ShowConfirm(&d, app.Content.Pages, "Confirm Flux action", message, func() {
		if app.Config.IsReadOnly() || app.Config.ActiveContextName() != contextName {
			app.Flash().Warn("Context or read-only mode changed; confirm the action again")
			return
		}
		app.Flash().Infof("Requesting Flux %s for %s", action, fqn)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			err := operation(ctx)
			if !app.IsRunning() {
				return
			}
			app.QueueUpdateDraw(func() {
				if err != nil {
					app.Flash().Errf("Flux %s failed for %s: %v", action, fqn, err)
					return
				}
				app.Flash().Infof("Flux %s requested for %s; watch STATUS for completion", action, fqn)
			})
		}()
	}, func() {})
	return nil
}

func (f *Flux) relatedCmd(evt *tcell.EventKey) *tcell.EventKey {
	o, _, err := f.selected()
	if err != nil {
		f.App().Flash().Err(err)
		return evt
	}
	refs := flux.Dependencies(o)
	options := make([]string, 0, len(refs)+1)
	for _, ref := range refs {
		options = append(options, fmt.Sprintf("Dependency: %s %s/%s", ref.Kind, ref.Namespace, ref.Name))
	}
	if source, ok := flux.Source(o); ok {
		refs = append([]flux.Reference{source}, refs...)
		options = append([]string{fmt.Sprintf("Source: %s %s/%s", source.Kind, source.Namespace, source.Name)}, options...)
	}
	if len(refs) == 0 {
		f.App().Flash().Info("No source or dependency references on this resource")
		return nil
	}
	d := f.App().Styles.Dialog()
	dialog.ShowSelection(&d, f.App().Content.Pages, "Flux relationships", options, func(index int) {
		if index < 0 || index >= len(refs) {
			return
		}
		ref := refs[index]
		gvr, err := resolveFluxReference(dao.MetaAccess, ref)
		if err != nil {
			f.App().Flash().Err(err)
			return
		}
		f.App().gotoResource(gvr.String()+" "+ref.Namespace, client.FQN(ref.Namespace, ref.Name), false, true)
	})
	return nil
}

func resolveFluxReference(metas *dao.Meta, ref flux.Reference) (*client.GVR, error) {
	best := client.NoGVR
	for _, gvr := range metas.AllGVRs() {
		if gvr.G() != ref.Group {
			continue
		}
		meta, err := metas.MetaFor(gvr)
		if err != nil || meta.Kind != ref.Kind {
			continue
		}
		if best == client.NoGVR || version.CompareKubeAwareVersionStrings(gvr.V(), best.V()) > 0 {
			best = gvr
		}
	}
	if best == client.NoGVR {
		return best, fmt.Errorf("%s in group %s is not available in this context", ref.Kind, ref.Group)
	}
	return best, nil
}
