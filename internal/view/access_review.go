// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"strings"

	"github.com/derailed/k9s/internal/access"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/ui/dialog"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

const accessFormPage = "access-question"
const accessCommandName = "access"

type accessView struct {
	*Details
	question             access.Question
	snapshot             *access.Snapshot
	generation, revision uint64
	cancel               context.CancelFunc
	modal                *ui.ModalForm
	cleanup              func()
}

func (c *Command) accessCommand(line string) {
	if strings.TrimSpace(line) != accessCommandName {
		c.app.Flash().Warn("Use :access to enter one explicit authorization question")
		return
	}
	q := access.Question{Context: c.app.Config.ActiveContextName(), GVR: "v1/pods", Verb: client.GetVerb, User: access.Self}
	namespace := c.app.Config.ActiveNamespace()
	if namespace != client.NamespaceAll && namespace != client.NotNamespaced && namespace != client.ClusterScope {
		q.Namespace = namespace
	}
	if selected, ok := c.app.Content.Top().(SelectedResource); ok {
		target := selected.SelectedResource()
		if target.Err() == nil && target.Context == q.Context {
			q.GVR = target.GVR.String()
			q.Namespace = target.Namespace
			q.Name = target.Name
			q.TargetUID = string(target.UID)
		}
	}
	v := &accessView{Details: NewDetails(c.app, "Access decision", "question", contentInspection, true), question: q, revision: c.app.Config.DestinationRevision()}
	if err := c.app.inject(v, false); err != nil {
		c.app.Flash().Err(err)
		return
	}
	v.Update(v.questionText() + "\nNo review requested. e: edit question; r: explicit review; Esc/q: return.\n" +
		"Review submission permission is separate from the questioned decision.")
	v.editQuestion()
}
func (v *accessView) Init(ctx context.Context) error {
	if err := v.Details.Init(ctx); err != nil {
		return err
	}
	v.actions.Add(ui.KeyE, ui.NewKeyAction("Edit access question", func(*tcell.EventKey) *tcell.EventKey { v.editQuestion(); return nil }, true))
	v.actions.Add(ui.KeyR, ui.NewKeyAction("Review explicit access question", func(*tcell.EventKey) *tcell.EventKey { v.runAccess(); return nil }, true))
	return nil
}
func (v *accessView) Draw(screen tcell.Screen) {
	if ui.DrawTaskSizeNotice(screen, v.Details.Flex.Box) {
		return
	}
	v.Details.Draw(screen)
}
func (v *accessView) Stop() {
	v.generation++
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
		body := v.questionText()
		if v.snapshot != nil {
			body = v.snapshot.Text() + "\nRetained previous review evidence.\n"
		}
		v.Update(body + "\nReview canceled; no new decision inferred. e: edit · r: explicit review · Esc/q: return\n")
	}
	v.dismissAccessForm(false)
	v.Details.Stop()
}
func (v *accessView) current(generation uint64) bool {
	return v.app.Content.Top() == v && v.generation == generation &&
		v.question.Context == v.app.Config.ActiveContextName() && v.revision == v.app.Config.DestinationRevision()
}
func (v *accessView) dismissAccessForm(focus bool) {
	if v.cleanup != nil {
		v.cleanup()
		v.cleanup = nil
	}
	if v.modal != nil && v.app.Content.Pages.GetPrimitive(accessFormPage) == v.modal {
		v.app.Content.Pages.RemovePage(accessFormPage)
	}
	v.modal = nil
	if focus && v.app.Content.Top() == v {
		v.app.SetFocus(v)
	}
}

type accessFormStyles struct {
	form  *tview.Form
	modal *ui.ModalForm
}

func (s *accessFormStyles) StylesChanged(styles *config.Styles) {
	d := styles.Dialog()
	dialog.StyleForm(&d, s.form)
	s.modal.SetDialogColors(&d)
}
func (v *accessView) editQuestion() {
	if v.app.Content.Top() != v {
		return
	}
	if !v.current(v.generation) {
		v.app.Flash().Warn("Destination changed; reopen :access for the current destination")
		return
	}
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	v.generation++
	v.dismissAccessForm(false)
	q := v.question
	groups := strings.Join(q.Groups, ",")
	cluster := q.Namespace == ""
	form := tview.NewForm().SetItemPadding(0).SetButtonsAlign(tview.AlignCenter)
	bounded := func(s string, _ rune) bool { return len(s) <= 512 }
	form.AddInputField("User (self or explicit)", q.User, 36, bounded, func(s string) { q.User = s })
	form.AddInputField("Explicit groups (comma)", groups, 36, bounded, func(s string) { groups = s })
	form.AddInputField("Verb", q.Verb, 36, bounded, func(s string) { q.Verb = s })
	form.AddInputField("GVR", q.GVR, 36, bounded, func(s string) { q.GVR = s })
	initial := 0
	if cluster {
		initial = 1
	}
	form.AddDropDown("Resource scope", []string{"namespaced", "cluster"}, initial, func(_ string, index int) { cluster = index == 1 })
	form.AddInputField("Namespace", q.Namespace, 36, bounded, func(s string) { q.Namespace = s })
	form.AddInputField("Subresource (optional)", q.Subresource, 36, bounded, func(s string) { q.Subresource = s })
	form.AddInputField("Name (optional)", q.Name, 36, bounded, func(s string) { q.Name = s })
	modal := ui.NewModalForm("Explicit access question", form)
	modal.SetContext("Context: " + q.Context)
	v.modal = modal
	generation := v.generation
	active := func() bool {
		_, front := v.app.Content.Pages.GetFrontPage()
		return v.modal == modal && v.current(generation) && front == modal
	}
	dismiss := func() {
		if v.modal == modal && v.app.Content.Top() == v && v.generation == generation {
			v.dismissAccessForm(true)
		}
	}
	form.AddButton("Cancel", dismiss).AddButton("Review", func() {
		if !active() {
			return
		}
		if cluster {
			q.Namespace = ""
		} else if strings.TrimSpace(q.Namespace) == "" {
			v.app.Flash().Warn("Namespaced questions require an explicit namespace")
			return
		}
		q.Groups = nil
		for _, group := range strings.Split(groups, ",") {
			if group = strings.TrimSpace(group); group != "" {
				q.Groups = append(q.Groups, group)
			}
		}
		if q.GVR != v.question.GVR || q.Namespace != v.question.Namespace || q.Name != v.question.Name {
			q.TargetUID = ""
		}
		if err := q.Validate(); err != nil {
			v.app.Flash().Err(err)
			return
		}
		v.question = q
		dismiss()
		v.runAccess()
	})
	modal.SetText("One SSAR for self, or SAR for the exact supplied user/groups. " +
		"Self identity review supports visible RBAC explanation. No identity enumeration or inferred group membership. " +
		"Cluster scope uses an empty namespace. Read access to bindings/roles is separately reported; candidate rules cannot prove unseen policy causes.")
	modal.SetDoneFunc(func(int, string) { dismiss() })
	listener := &accessFormStyles{form: form, modal: modal}
	listener.StylesChanged(v.app.Styles)
	v.app.Styles.AddListener(listener)
	v.cleanup = func() { v.app.Styles.RemoveListener(listener) }
	v.app.Content.Pages.AddPage(accessFormPage, modal, false, true)
	v.app.SetFocus(modal)
}
func (v *accessView) runAccess() {
	if v.app.Content.Top() != v || v.modal != nil {
		return
	}
	if !v.current(v.generation) {
		v.app.Flash().Warn("Destination changed; reopen :access for the current destination")
		return
	}
	if err := v.question.Validate(); err != nil {
		v.app.Flash().Err(err)
		return
	}
	conn, err := pinInspectionConnection(v.app.Conn())
	if err != nil {
		v.app.Flash().Err(err)
		return
	}
	if v.cancel != nil {
		v.cancel()
	}
	v.generation++
	generation := v.generation
	captured := v.question
	ctx, cancel := context.WithTimeout(context.Background(), access.Timeout)
	v.cancel = cancel
	v.Update(v.questionText() + "\nReviewing one explicit question; bounded RBAC explanation follows.\n" +
		"Esc/q returns and cancels. No questioned resource is read or changed.")
	go func() {
		defer cancel()
		typed, err := conn.Dial()
		var snapshot *access.Snapshot
		if err == nil {
			snapshot = access.Collect(ctx, typed, &captured)
		}
		if !v.app.IsRunning() {
			return
		}
		v.app.QueueUpdateDraw(func() {
			if !v.current(generation) {
				return
			}
			v.cancel = nil
			if err != nil {
				v.Update(v.questionText() + "\nAccess review unavailable: captured API connection could not be opened.\n" +
					"Previous evidence retained; no decision inferred. e: edit · r: explicit review · Esc/q: return\n")
				return
			}
			v.snapshot = snapshot
			v.Update(snapshot.Text() + "\ne: edit · r: explicit review · Esc/q: return\n")
		})
	}()
}

func (v *accessView) questionText() string {
	q := &v.question
	scope := q.Namespace
	if scope == "" {
		scope = "cluster"
	}
	return fmt.Sprintf("ACCESS QUESTION\nSubject: %s\nVerb: %s\nResource: %s\nScope: %s\nSubresource/name: %s / %s\nContext: %s\n",
		q.User, q.Verb, q.GVR, scope, q.Subresource, q.Name, q.Context)
}
