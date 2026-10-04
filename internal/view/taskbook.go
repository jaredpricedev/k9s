// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/provider"
	"github.com/derailed/k9s/internal/taskbook"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/ui/dialog"
	"github.com/derailed/k9s/internal/workspace"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type taskbookView struct {
	*Details
	artifact   taskbook.Artifact
	ready      bool
	generation uint64
	cancel     context.CancelFunc
	form       *ui.ModalForm
	cleanup    func()
}

func (v *taskbookView) Init(ctx context.Context) error {
	if err := v.Details.Init(ctx); err != nil {
		return err
	}
	v.actions.Add(ui.KeyS, ui.NewKeyAction("Save task handoff", func(*tcell.EventKey) *tcell.EventKey { v.saveTask(); return nil }, true))
	v.actions.Add(ui.KeyR, ui.NewKeyAction("Explicitly rerun fixed reads", func(*tcell.EventKey) *tcell.EventKey { v.confirmRerun(); return nil }, true))
	return nil
}
func (v *taskbookView) Draw(screen tcell.Screen) {
	if ui.DrawTaskSizeNotice(screen, v.Details.Flex.Box) {
		return
	}
	v.Details.Draw(screen)
}

func (v *taskbookView) Stop() {
	v.generation++
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	v.dismissTaskForm(false)
	v.Details.Stop()
}
func (v *taskbookView) renderTask() {
	text, err := taskbook.Preview(v.artifact)
	if err != nil {
		v.ready = false
		v.Update("Task handoff unavailable: " + err.Error())
		return
	}
	v.ready = true
	v.Update(text)
}

func (c *Command) taskbookCommand(line string) {
	parts := strings.SplitN(strings.TrimSpace(line), " ", 3)
	if len(parts) < 2 {
		c.app.Flash().Warn("Use :taskbook save from retained evidence/workspace, or :taskbook open /absolute/path.json")
		return
	}
	v := &taskbookView{Details: NewDetails(c.app, "Task handoff", "offline", contentInspection, true)}
	switch parts[1] {
	case "open":
		if len(parts) != 3 || strings.TrimSpace(parts[2]) == "" {
			c.app.Flash().Warn("Use :taskbook open /absolute/path.json")
			return
		}
		if err := c.app.inject(v, false); err != nil {
			c.app.Flash().Err(err)
			return
		}
		v.Update("Opening bounded offline task; no checks running...")
		path := strings.TrimSpace(parts[2])
		generation := v.generation
		go func() {
			a, err := taskbook.Read(path)
			if !v.app.IsRunning() {
				return
			}
			v.app.QueueUpdateDraw(func() {
				if !v.acceptTaskResult(generation, "") {
					return
				}
				if err != nil {
					v.Update("Offline task unavailable: " + err.Error())
					return
				}
				v.artifact = a
				v.renderTask()
			})
		}()
	case taskbookSaveToken:
		if len(parts) != 2 {
			c.app.Flash().Warn("Use :taskbook save; choose fields in the form")
			return
		}
		evidence, scopes, err := taskbookEvidence(c.app.Content.Top())
		if err != nil {
			c.app.Flash().Err(err)
			return
		}
		a, err := taskbook.New("Investigate retained evidence", evidence)
		if err != nil {
			c.app.Flash().Err(err)
			return
		}
		a.Scopes = scopes
		v.artifact = a
		if err = c.app.inject(v, false); err != nil {
			c.app.Flash().Err(err)
			return
		}
		v.renderTask()
		v.saveTask()
	default:
		c.app.Flash().Warn("Use :taskbook save or :taskbook open /absolute/path.json")
	}
}

func taskbookEvidence(owner any) (inspect.Bundle, []workspace.Scope, error) {
	switch v := owner.(type) {
	case *evidenceView:
		if !v.ready {
			return inspect.Bundle{}, nil, fmt.Errorf("wait for retained evidence")
		}
		b, err := inspect.SafeBundle(v.bundle)
		return b, nil, err
	case *taskbookView:
		b, err := inspect.SafeBundle(v.artifact.Evidence)
		return b, v.artifact.Scopes, err
	case *dailyWorkspace:
		if v.snapshot.ObservedAt.IsZero() || v.scope.Name == "" {
			return inspect.Bundle{}, nil, fmt.Errorf("explicitly refresh a saved workspace first")
		}
		observations := make([]inspect.Observation, 0, 8)
		var text strings.Builder
		for _, coverage := range v.snapshot.Coverage {
			fmt.Fprintf(&text, "Coverage: %+v\n", coverage)
		}
		for _, r := range v.snapshot.Resources[:min(8, len(v.snapshot.Resources))] {
			// Summaries intentionally omit Resource.Object, which was not collected
			// through the portable evidence sanitizer.
			observations = append(observations, inspect.Observation{
				Identity: inspect.ResourceIdentity{Context: v.scope.Context, GVR: r.Ref.GVR, Namespace: r.Ref.Namespace, Name: r.Ref.Name, UID: r.Ref.UID},
				Source:   "Retained daily workspace summary", ObservedAt: v.snapshot.ObservedAt, State: inspect.ObservationIncomplete,
				Reason: r.Summary, Limits: []string{"Summary only; no raw workspace object retained"}})
		}
		if len(observations) == 0 {
			return inspect.Bundle{}, nil, fmt.Errorf("workspace has no retained named resource; use evidence preview for unavailable targets")
		}
		b := inspect.NewBundle(observations)
		b.Limits = append(b.Limits, "At most first 8 retained resources; workspace may contain additional resources. No collection rerun.")
		coverageText := text.String()
		if len(coverageText) > 8192 {
			coverageText = strings.ToValidUTF8(coverageText[:8192], "")
			b.Limits = append(b.Limits, "Workspace coverage text truncated at 8 KiB")
		}
		if coverageText != "" {
			b.Snippets = []inspect.Snippet{{Source: "Retained daily workspace coverage", ObservedAt: v.snapshot.ObservedAt,
				Text: coverageText, Limits: "Original per-query coverage; no API rerun"}}
		}
		b, err := inspect.SafeBundle(b)
		return b, []workspace.Scope{v.scope}, err
	default:
		b, err := retainedEvidenceBundle(owner)
		return b, nil, err
	}
}

func (v *taskbookView) acceptTaskResult(generation uint64, contextName string) bool {
	return v.app.Content.Top() == v && v.generation == generation && (contextName == "" || v.app.Config.ActiveContextName() == contextName)
}

func (v *taskbookView) acceptTaskRerun(generation uint64, contextName string, revision uint64) bool {
	return v.acceptTaskResult(generation, contextName) && revision == v.app.Config.DestinationRevision()
}

const taskbookFormPage = "taskbook-form"
const taskbookSaveToken = "save"

func (v *taskbookView) dismissTaskForm(focus bool) {
	if v.cleanup != nil {
		v.cleanup()
		v.cleanup = nil
	}
	if v.form != nil && v.app.Content.Pages.GetPrimitive(taskbookFormPage) == v.form {
		v.app.Content.Pages.RemovePage(taskbookFormPage)
	}
	v.form = nil
	if focus && v.app.Content.Top() == v {
		v.app.SetFocus(v)
	}
}

type taskbookFormStyles struct {
	form  *tview.Form
	modal *ui.ModalForm
}

func (s *taskbookFormStyles) StylesChanged(styles *config.Styles) {
	d := styles.Dialog()
	dialog.StyleForm(&d, s.form)
	s.modal.SetBackgroundColor(d.BgColor.Color()).SetTextColor(d.FgColor.Color())
}

func (v *taskbookView) showTaskForm(title, message string, build func(*tview.Form, func() bool, func())) {
	if !v.ready || v.app.Content.Top() != v {
		return
	}
	v.dismissTaskForm(false)
	form := tview.NewForm().SetItemPadding(0).SetButtonsAlign(tview.AlignCenter)
	modal := ui.NewModalForm(title, form)
	generation := v.generation
	v.form = modal
	active := func() bool {
		_, front := v.app.Content.Pages.GetFrontPage()
		return v.form == modal && v.acceptTaskResult(generation, "") && front == modal
	}
	dismiss := func() {
		if active() {
			v.dismissTaskForm(true)
		}
	}
	build(form, active, dismiss)
	modal.SetText(message).SetDoneFunc(func(int, string) { dismiss() })
	listener := &taskbookFormStyles{form: form, modal: modal}
	listener.StylesChanged(v.app.Styles)
	v.app.Styles.AddListener(listener)
	v.cleanup = func() { v.app.Styles.RemoveListener(listener) }
	v.app.Content.Pages.AddPage(taskbookFormPage, modal, false, true)
	v.app.SetFocus(modal)
}

func (v *taskbookView) saveTask() {
	summary := v.artifact.Summary
	questions := strings.Join(v.artifact.Questions, " | ")
	links := strings.Join(v.artifact.Links, " | ")
	var path, providers string
	v.showTaskForm("Save task handoff", "New private .json file. Original evidence retained. Questions/links separated by |. "+
		"Optional version checks: kubectl-version, helm-version, flux-version, cilium-version. Review heuristic redaction before sharing.",
		func(form *tview.Form, active func() bool, dismiss func()) {
			bounded := func(s string, _ rune) bool { return len(s) <= 4096 }
			form.AddInputField("New absolute path", "", 36, bounded, func(s string) { path = s })
			form.AddInputField("Summary", summary, 36, bounded, func(s string) { summary = s })
			form.AddInputField("Questions", questions, 36, bounded, func(s string) { questions = s })
			form.AddInputField("Evidence links", links, 36, bounded, func(s string) { links = s })
			form.AddInputField("Add version checks", "", 36, bounded, func(s string) { providers = s })
			form.AddButton("Cancel", dismiss).AddButton("Save", func() {
				if !active() {
					return
				}
				next := v.artifact
				next.Summary = summary
				next.Questions = taskbookTextList(questions)
				next.Links = taskbookTextList(links)
				next.Checks = append([]taskbook.Check(nil), next.Checks...)
				for _, id := range strings.FieldsFunc(providers, func(r rune) bool { return r == ',' || r == ' ' }) {
					if id == taskbook.ResourceGet {
						v.app.Flash().Warn("Only optional client version IDs belong in this field")
						return
					}
					next.Checks = append(next.Checks, taskbook.Check{ID: id, Observation: 0})
				}
				safe, err := taskbook.Safe(next)
				if err != nil {
					v.app.Flash().Err(err)
					return
				}
				destination := strings.TrimSpace(path)
				if destination == "" {
					v.app.Flash().Warn("Choose an absolute new .json file")
					return
				}
				v.artifact = safe
				dismiss()
				v.renderTask()
				generation := v.generation
				go func() {
					err := taskbook.Save(destination, safe)
					if !v.app.IsRunning() {
						return
					}
					v.app.QueueUpdateDraw(func() {
						if !v.acceptTaskResult(generation, "") {
							return
						}
						if err != nil {
							v.app.Flash().Err(err)
						} else {
							v.app.Flash().Infof("Saved task handoff: %s (0600)", destination)
						}
					})
				}()
			})
		})
}
func taskbookTextList(text string) []string {
	var out []string
	for _, s := range strings.Split(text, "|") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (v *taskbookView) confirmRerun() {
	v.showTaskForm("Explicit read-only rerun", "Rerun only listed fixed checks in the recorded current context. Saved UID required for API reads. "+
		"Secret kinds excluded. Original evidence remains separate. Cancel performs no reads.", func(form *tview.Form, active func() bool, dismiss func()) {
		form.AddButton("Cancel", dismiss).AddButton("Rerun checks", func() {
			if !active() {
				return
			}
			dismiss()
			v.rerunTask()
		})
	})
}
func (v *taskbookView) rerunTask() {
	if !v.ready || v.app.Content.Top() != v {
		return
	}
	captured, err := taskbook.Safe(v.artifact)
	if err != nil {
		v.app.Flash().Err(err)
		return
	}
	contextName := v.app.Config.ActiveContextName()
	revision := v.app.Config.DestinationRevision()
	destinationScope := provider.Scope{Context: contextName, Namespace: v.app.Config.ActiveNamespace(), Revision: revision}
	var conn client.Connection
	needsAPI := false
	for _, check := range captured.Checks {
		if check.ID == taskbook.ResourceGet {
			identity := captured.Evidence.Observations[check.Observation].Identity
			if identity.Context == contextName && identity.UID != "" && !strings.HasSuffix(identity.GVR, "/secrets") {
				needsAPI = true
			}
		}
	}
	if needsAPI {
		conn, err = pinInspectionConnection(v.app.Conn())
		if err != nil {
			v.app.Flash().Err(err)
			return
		}
	}
	if v.cancel != nil {
		v.cancel()
	}
	v.generation++
	generation := v.generation
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(max(1, len(captured.Checks)))*8*time.Second)
	v.cancel = cancel
	v.Update("Explicit checks running; original evidence retained. q/Esc returns and cancels.")
	var reader taskbook.Reader
	if conn != nil {
		reader = taskbookAPIReader(conn)
	}
	go func() {
		defer cancel()
		results, err := taskbook.Rerun(ctx, captured, destinationScope, reader, nil)
		if !v.app.IsRunning() {
			return
		}
		v.app.QueueUpdateDraw(func() {
			if !v.acceptTaskRerun(generation, contextName, revision) {
				return
			}
			v.cancel = nil
			if err != nil {
				v.Update("Task rerun unavailable: " + err.Error())
				return
			}
			v.artifact.Latest = results
			v.renderTask()
		})
	}()
}
func taskbookAPIReader(conn client.Connection) taskbook.Reader {
	return func(ctx context.Context, identity inspect.ResourceIdentity) inspect.Observation {
		o := inspect.Observation{Identity: identity, Source: "Kubernetes API GET (explicit taskbook rerun)", ObservedAt: time.Now().UTC(), State: inspect.ObservationUnknown}
		if identity.UID == "" {
			o.State = inspect.ObservationIncomplete
			o.Reason = "Saved UID required before API read"
			return o
		}
		// Defense in depth before dialing, including imported Secret targets.
		if strings.HasSuffix(strings.ToLower(identity.GVR), "/secrets") {
			o.State = inspect.ObservationIncomplete
			o.Reason = "Secret kinds excluded before API read"
			return o
		}
		dyn, err := conn.DynDial()
		if err != nil {
			o.Reason = "Captured API connection unavailable"
			return o
		}
		obj, err := dyn.Resource(client.NewGVR(identity.GVR).GVR()).Namespace(identity.Namespace).Get(ctx, identity.Name, metav1.GetOptions{})
		if err != nil {
			o.Reason = "API read unavailable"
			if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
				o.State = inspect.ObservationDenied
				o.Reason = "Permission denied"
			} else if apierrors.IsNotFound(err) {
				o.State = inspect.ObservationStale
				o.Reason = "Saved resource no longer exists"
			}
			return o
		}
		if string(obj.GetUID()) != identity.UID {
			o.State = inspect.ObservationStale
			o.Reason = "Resource UID changed; replacement body excluded"
			return o
		}
		o.State = inspect.ObservationComplete
		o.Object = obj.Object
		return inspect.SafeObservation(o)
	}
}
