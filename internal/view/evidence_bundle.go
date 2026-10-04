// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/ui/dialog"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
)

type evidenceView struct {
	*Details
	bundle     inspect.Bundle
	ready      bool
	cancel     context.CancelFunc
	generation uint64
	forms      map[string]*evidenceForm
}

type evidenceForm struct {
	modal      *tview.ModalForm
	cleanup    func()
	generation uint64
}

func (v *evidenceView) Stop() {
	for page, form := range v.forms {
		v.dismissForm(page, form, false)
	}
	v.generation++
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	v.Details.Stop()
}

func (v *evidenceView) formActive(page string, form *evidenceForm) bool {
	_, front := v.app.Content.Pages.GetFrontPage()
	return v.ready && v.app.Content.Top() == v && v.generation == form.generation &&
		v.forms[page] == form && v.app.Content.Pages.GetPrimitive(page) == form.modal && front == form.modal
}

func (v *evidenceView) dismissForm(page string, form *evidenceForm, focus bool) {
	if v.forms[page] != form {
		return
	}
	form.cleanup()
	delete(v.forms, page)
	if v.app.Content.Pages.GetPrimitive(page) == form.modal {
		v.app.Content.Pages.RemovePage(page)
	}
	if focus && v.app.Content.Top() == v && v.generation == form.generation {
		v.app.SetFocus(v)
	}
}

func (v *evidenceView) showForm(page string, form *tview.Form, state *evidenceForm) {
	if v.forms == nil {
		v.forms = make(map[string]*evidenceForm)
	}
	if previous := v.forms[page]; previous != nil {
		v.dismissForm(page, previous, false)
	}
	state.cleanup = dialog.BindFormStyles(v.app.Styles, form, state.modal)
	v.forms[page] = state
	v.app.Content.Pages.AddPage(page, state.modal, false, true)
	v.app.SetFocus(state.modal)
}

func (v *evidenceView) Init(ctx context.Context) error {
	if err := v.Details.Init(ctx); err != nil {
		return err
	}
	v.actions.Add(ui.KeyN, ui.NewKeyAction("Add investigation note", func(*tcell.EventKey) *tcell.EventKey { v.noteForm(false); return nil }, true))
	v.actions.Add(ui.KeyL, ui.NewKeyAction("Add selected redacted snippet", func(*tcell.EventKey) *tcell.EventKey { v.noteForm(true); return nil }, true))
	v.actions.Add(ui.KeyS, ui.NewKeyAction("Save previewed evidence", func(*tcell.EventKey) *tcell.EventKey { v.saveForm(); return nil }, true))
	return nil
}

func (v *evidenceView) renderBundle() {
	preview, err := inspect.BundlePreview(v.bundle)
	if err != nil {
		v.ready = false
		v.Update("Evidence unavailable: " + err.Error())
		return
	}
	v.ready = true
	v.Update(preview)
}

func (c *Command) evidenceCommand(line string) {
	parts := strings.SplitN(strings.TrimSpace(line), " ", 2)
	if parts[0] == "evidence-open" {
		if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
			c.app.Flash().Err(fmt.Errorf("usage: :evidence-open /absolute/path.json (or .md); offline inspection"))
			return
		}
		c.openOfflineEvidence(strings.TrimSpace(parts[1]))
		return
	}
	switch owner := c.app.Content.Top().(type) {
	case *comparisonView, *inspectionDetails:
		bundle, err := retainedEvidenceBundle(owner)
		if err != nil {
			c.app.Flash().Err(err)
			return
		}
		v := &evidenceView{Details: NewDetails(c.app, "Retained evidence preview", "retained", contentInspection, true), bundle: bundle}
		if err := c.app.inject(v, false); err != nil {
			c.app.Flash().Err(err)
			return
		}
		v.renderBundle()
		return
	}
	owner, ok := c.app.Content.Top().(ResourceViewer)
	if !ok {
		c.app.Flash().Err(fmt.Errorf("select a resource to capture evidence; use :evidence-open for offline bundles"))
		return
	}
	target := resolveSelectedResource(owner, c.app.Config.ActiveContextName())
	if err := target.Err(); err != nil {
		c.app.Flash().Err(err)
		return
	}
	conn, err := pinInspectionConnection(c.app.Conn())
	if err != nil {
		c.app.Flash().Err(err)
		return
	}
	v := &evidenceView{Details: NewDetails(c.app, "Evidence preview", target.Path(), contentInspection, true).
		Update("Capturing selected resource and UID-scoped events; no file saved...")}
	if err := c.app.inject(v, false); err != nil {
		c.app.Flash().Err(err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	v.cancel = cancel
	v.generation++
	generation := v.generation
	go func() {
		defer cancel()
		bundle, err := loadEvidenceBundle(ctx, conn, target)
		if !v.app.IsRunning() {
			return
		}
		v.app.QueueUpdateDraw(func() {
			if v.app.Content.Top() != v || generation != v.generation || target.Context != v.app.Config.ActiveContextName() {
				return
			}
			if err != nil {
				v.Update("Evidence unavailable: " + err.Error())
				return
			}
			v.bundle = bundle
			v.renderBundle()
		})
	}()
}

func (c *Command) openOfflineEvidence(path string) {
	v := &evidenceView{Details: NewDetails(c.app, "Offline evidence", "import", contentInspection, true).
		Update("Reading bounded offline bundle...")}
	if err := c.app.inject(v, false); err != nil {
		c.app.Flash().Err(err)
		return
	}
	v.generation++
	generation := v.generation
	go func() {
		bundle, err := inspect.ReadBundle(path)
		if !v.app.IsRunning() {
			return
		}
		v.app.QueueUpdateDraw(func() {
			if v.app.Content.Top() != v || generation != v.generation {
				return
			}
			if err != nil {
				v.Update("Offline evidence unavailable: " + err.Error())
				return
			}
			v.bundle = bundle
			v.renderBundle()
		})
	}()
}

func retainedEvidenceBundle(owner any) (inspect.Bundle, error) {
	switch v := owner.(type) {
	case *comparisonView:
		if v.baseline == nil {
			return inspect.Bundle{}, fmt.Errorf("wait for retained comparison baseline A before exporting evidence")
		}
		observations := []inspect.Observation{*v.baseline}
		bundle := inspect.NewBundle(observations)
		bundle.Notes = []string{"Observation 1 is the chosen comparison baseline A; its original source and capture time are retained."}
		if !v.other.ObservedAt.IsZero() {
			bundle.Observations = append(bundle.Observations, v.other)
			bundle.Notes = append(bundle.Notes, "Observation 2 is retained comparison B; export does not capture a newer object.")
		} else {
			bundle.Limits = append(bundle.Limits, "Comparison B has not been captured; only baseline A is included.")
		}
		return inspect.SafeBundle(bundle)
	case *inspectionDetails:
		if v.snapshot.Text == "" || v.snapshot.CapturedAt.IsZero() {
			return inspect.Bundle{}, fmt.Errorf("wait for a retained inspection snapshot before exporting evidence")
		}
		if err := v.target.Err(); err != nil {
			return inspect.Bundle{}, err
		}
		identity := inspect.ResourceIdentity{Context: v.target.Context, GVR: v.target.GVR.String(),
			Namespace: v.target.Namespace, Name: v.target.Name, UID: string(v.snapshot.UID)}
		observation := inspect.Observation{Identity: identity, Source: "Retained " + v.title + " inspection snapshot",
			ObservedAt: v.snapshot.CapturedAt, State: inspect.ObservationIncomplete,
			Reason: "Retained inspection text only; the raw resource object was not retained"}
		bundle := inspect.NewBundle([]inspect.Observation{observation})
		text := v.snapshot.Text
		limits := "Retained captured inspection text; export does not refresh the resource. This is not a complete history."
		if len(text) > 8<<10 {
			text = strings.ToValidUTF8(text[:8<<10], "")
			limits += " Text truncated at 8 KiB; remaining snapshot text is not included."
		}
		bundle.Snippets = []inspect.Snippet{{Source: observation.Source, ObservedAt: v.snapshot.CapturedAt, Text: text, Limits: limits}}
		return inspect.SafeBundle(bundle)
	default:
		return inspect.Bundle{}, fmt.Errorf("this view has no retained resource observation")
	}
}

//nolint:gocritic // Capture owns an immutable target value while API work runs asynchronously.
func loadEvidenceBundle(ctx context.Context, conn client.Connection, target SelectedResourceTarget) (inspect.Bundle, error) {
	if err := target.Err(); err != nil {
		return inspect.Bundle{}, err
	}
	if target.GVR.G() == "" && target.GVR.R() == inspectionSecretsResource {
		observation := loadResourceObservation(ctx, conn, target)
		return inspect.SafeBundle(inspect.NewBundle([]inspect.Observation{observation}))
	}
	dyn, err := conn.DynDial()
	if err != nil {
		return inspect.Bundle{}, err
	}
	obj, err := dyn.Resource(target.GVR.GVR()).Namespace(target.Namespace).Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return inspect.Bundle{}, err
	}
	identityErr := verifySelectedIdentity(target, obj)
	if identityErr != nil {
		return inspect.Bundle{}, identityErr
	}
	identity := inspect.ResourceIdentity{Context: target.Context, GVR: target.GVR.String(), Namespace: target.Namespace, Name: target.Name, UID: string(obj.GetUID())}
	observation := inspect.NewObservation(identity, "Kubernetes API GET (explicit evidence capture)", time.Now().UTC(), obj.Object)
	bundle := inspect.NewBundle([]inspect.Observation{observation})
	if obj.GetUID() == "" {
		bundle.Limits = append(bundle.Limits, "UID unknown; retained events cannot be safely scoped.")
		return inspect.SafeBundle(bundle)
	}
	typed, err := conn.Dial()
	if err != nil {
		bundle.Limits = append(bundle.Limits, "Events unavailable: "+err.Error())
		return inspect.SafeBundle(bundle)
	}
	events, err := typed.CoreV1().Events(target.Namespace).List(ctx, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("involvedObject.uid", string(obj.GetUID())).String(), Limit: 100,
	})
	if err != nil {
		bundle.Limits = append(bundle.Limits, "Events unavailable: "+err.Error())
		return inspect.SafeBundle(bundle)
	}
	var text strings.Builder
	truncated := events.Continue != "" || len(events.Items) > 100
	for i := range events.Items[:min(100, len(events.Items))] {
		e := &events.Items[i]
		// A nonconforming API must not broaden the server-side UID selector.
		if e.InvolvedObject.UID != obj.GetUID() {
			continue
		}
		// Sanitize the bounded aggregate at SafeBundle: removing a PEM header
		// from one message first would expose its continuation in later events.
		line := fmt.Sprintf("%s %s %s count=%d: %s\n", eventTime(e).UTC().Format(time.RFC3339), e.Type, e.Reason, e.Count, e.Message)
		if text.Len()+len(line) > 8<<10 {
			truncated = true
			break
		}
		text.WriteString(line)
	}
	limits := "At most 100 UID-scoped retained events; not a complete history. Missing events do not establish health."
	if truncated {
		limits += " Results truncated by API/page/text limit."
	}
	if text.Len() == 0 {
		text.WriteString("No retained matching events reported; absence does not establish health.")
	}
	bundle.Snippets = []inspect.Snippet{{Source: "Kubernetes Events API; involvedObject.uid=" + string(obj.GetUID()),
		ObservedAt: time.Now().UTC(), Text: text.String(), Limits: limits}}
	return inspect.SafeBundle(bundle)
}

func (v *evidenceView) noteForm(snippet bool) {
	if v.app.Content.Top() != v {
		return
	}
	if !v.ready {
		v.app.Flash().Warn("Wait for an available evidence preview first")
		return
	}
	var value, source string
	form := tview.NewForm().SetItemPadding(0).SetButtonsAlign(tview.AlignCenter)
	styles := v.app.Styles.Dialog()
	form.SetButtonBackgroundColor(styles.ButtonBgColor.Color()).SetButtonTextColor(styles.ButtonFgColor.Color()).
		SetLabelColor(styles.LabelFgColor.Color()).SetFieldTextColor(styles.FieldFgColor.Color()).SetFieldBackgroundColor(styles.BgColor.Color())
	title, label := "Investigation note", "Note"
	if snippet {
		title, label = "Selected snippet", "Text"
		form.AddInputField("Named source", "", 36, nil, func(s string) { source = s })
	}
	form.AddInputField(label, "", 48, func(s string, _ rune) bool { return len(s) <= 8<<10 }, func(s string) { value = s })
	const page = "evidence-note"
	modal := tview.NewModalForm(title, form)
	state := &evidenceForm{modal: modal, generation: v.generation}
	dismiss := func() {
		if v.formActive(page, state) {
			v.dismissForm(page, state, true)
		}
	}
	form.AddButton("Cancel", dismiss).AddButton("Add to preview", func() {
		if !v.formActive(page, state) {
			return
		}
		if strings.TrimSpace(value) == "" || (snippet && strings.TrimSpace(source) == "") {
			v.app.Flash().Warn("Text and a named snippet source are required")
			return
		}
		next := v.bundle
		if snippet {
			next.Snippets = append(append([]inspect.Snippet(nil), next.Snippets...), inspect.Snippet{
				Source: source, ObservedAt: time.Now().UTC(), Text: value,
				Limits: "Operator-selected snippet; timestamp records selection, not original event time; redaction is heuristic.",
			})
		} else {
			next.Notes = append(append([]string(nil), next.Notes...), value)
		}
		clean, err := inspect.SafeBundle(next)
		if err != nil {
			v.app.Flash().Err(err)
			return
		}
		v.bundle = clean
		dismiss()
		v.renderBundle()
	})
	modal.SetText("Only this explicit text is added. Preview heuristic redaction before exporting.")
	modal.SetDoneFunc(func(int, string) { dismiss() })
	v.showForm(page, form, state)
}

func (v *evidenceView) saveForm() {
	if v.app.Content.Top() != v {
		return
	}
	if !v.ready {
		v.app.Flash().Warn("Wait for an available evidence preview first")
		return
	}
	var path string
	styles := v.app.Styles.Dialog()
	form := tview.NewForm().SetButtonsAlign(tview.AlignCenter).
		SetButtonBackgroundColor(styles.ButtonBgColor.Color()).SetButtonTextColor(styles.ButtonFgColor.Color()).
		SetLabelColor(styles.LabelFgColor.Color()).SetFieldTextColor(styles.FieldFgColor.Color()).SetFieldBackgroundColor(styles.BgColor.Color())
	form.AddInputField("New absolute .json/.md path", "", 48, nil, func(s string) { path = s })
	const page = "evidence-save"
	modal := tview.NewModalForm("Export evidence", form)
	state := &evidenceForm{modal: modal, generation: v.generation}
	dismiss := func() {
		if v.formActive(page, state) {
			v.dismissForm(page, state, true)
		}
	}
	form.AddButton("Cancel", dismiss).AddButton("Save previewed evidence", func() {
		if !v.formActive(page, state) {
			return
		}
		bundle, err := inspect.SafeBundle(v.bundle)
		if err != nil {
			v.app.Flash().Err(err)
			return
		}
		destination := strings.TrimSpace(path)
		if destination == "" {
			v.app.Flash().Warn("Choose an absolute new file path")
			return
		}
		dismiss()
		generation := v.generation
		go func() {
			err := inspect.SaveBundle(destination, bundle)
			if !v.app.IsRunning() {
				return
			}
			v.app.QueueUpdateDraw(func() {
				if v.app.Content.Top() != v || v.generation != generation {
					return
				}
				if err != nil {
					v.app.Flash().Err(err)
				} else {
					v.app.Flash().Infof("Saved evidence: %s (0600)", destination)
				}
			})
		}()
	})
	modal.SetText("Review the preview before saving. Secret bodies excluded; redaction is heuristic. Creates a new 0600 file; existing files are never overwritten.")
	modal.SetDoneFunc(func(int, string) { dismiss() })
	v.showForm(page, form, state)
}
