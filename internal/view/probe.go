// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/probe"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"k8s.io/apimachinery/pkg/types"
)

const (
	probeCommandToken = "probe"
	probeTitle        = "Probe"
	probeActionID     = "resource.probe"
	// probeKey opens the probe from a Service, workload or Pod row.
	probeKey = ui.KeyShiftB
	// probeSlowKey opens the sibling :slow view when the build has one.
	probeSlowKey     = ui.KeyW
	probeSlowCommand = "slow"
)

var probeKinds = map[string]string{
	client.SvcGVR.String(): probe.KindService, client.DpGVR.String(): probe.KindDeployment,
	client.StsGVR.String(): probe.KindStatefulSet, client.DsGVR.String(): probe.KindDaemonSet,
	client.RsGVR.String(): probe.KindReplicaSet, client.PodGVR.String(): probe.KindPod,
}

// probeView times one declared, read-only GET to every Pod behind a
// selection. Opening it reads Kubernetes objects only. A request leaves
// only after r and a confirmation of exactly what will be sent; it never
// repeats on its own.
type probeView struct {
	*Details
	contextName string
	revision    uint64
	scope       probe.Scope
	loadPlan    func(context.Context) (*probe.Plan, error)
	transport   probe.Transport
	// pause is the gap between two requests to one Pod.
	pause time.Duration

	plan     *probe.Plan
	planErr  string
	attempts int
	// confirmed is the signature of the last request shape the user agreed
	// to. Another count, path or Pod set asks again.
	confirmed string
	// run is the current run and previous the one before it, both owned
	// by the UI goroutine. A running probe reports each answer into run.
	run, previous     *probe.Run
	ranAt, previousAt time.Time
	elapsed           time.Duration
	loading, running  bool
	active            bool
	cancel            context.CancelFunc
	generation        uint64
	header, detail    *tview.TextView
	footer            *tview.TextView
	body              *tview.TextView
	// rowOf is the body line of each target; bodyHeight is the number of
	// body lines on screen, bodyLines how many there are and offset the
	// first one shown.
	rowOf                         []int
	bodyHeight, bodyLines, offset int
	selected                      string
	width, height                 int
	headerHeight                  int
	detailHeight                  int
	// slowPresent reports whether this build registers the :slow command.
	slowPresent func() bool
	// handoff makes SelectedResource answer with the selection the view was
	// opened for while a command is handed on to :slow.
	handoff bool
	// rendering guards render against re-entry.
	rendering bool
	// confirm is the open question, or nil.
	confirm *probeConfirm
}

// probeScope says whether a selection can be probed, and as what.
//
//nolint:gocritic // The immutable selection is captured by value.
func probeScope(target SelectedResourceTarget) (probe.Scope, error) {
	if target.Err() != nil || target.GVR == nil {
		return probe.Scope{}, errors.New("Select a Service, a workload or a Pod to probe its Pods")
	}
	kind, ok := probeKinds[target.GVR.String()]
	if !ok || target.Namespace == "" || target.Name == "" {
		return probe.Scope{}, errors.New("Probe times the Pods behind a Service, Deployment, StatefulSet, DaemonSet, ReplicaSet or one Pod")
	}
	return probe.Scope{Kind: kind, Namespace: target.Namespace, Name: target.Name, UID: string(target.UID)}, nil
}

func (c *Command) probeCommand() {
	target := SelectedResourceTarget{Context: c.app.Config.ActiveContextName(), UnavailableReason: "no selection"}
	if owner, ok := c.app.Content.Top().(actionOwner); ok {
		target = actionTarget(owner, c.app.Config.ActiveContextName())
	}
	c.app.openProbe(target)
}

// openProbe opens the view for a selection. It captures the connection on
// the UI thread and sends nothing: the plan is read in the background and a
// request leaves only on r.
//
//nolint:gocritic // The view owns a captured selection independently of navigation.
func (a *App) openProbe(target SelectedResourceTarget) {
	scope, err := probeScope(target)
	if err != nil {
		a.Flash().Warn(err.Error())
		return
	}
	if target.Context != "" && target.Context != a.Config.ActiveContextName() {
		a.Flash().Warn("Context changed; select the resource again before probing")
		return
	}
	connection, err := pinInspectionConnection(a.Conn())
	if err != nil {
		a.Flash().Err(err)
		return
	}
	cfg, err := kubeRESTConfig(a)()
	if err != nil {
		a.Flash().Err(err)
		return
	}
	transport, err := probe.NewAPITransport(cfg)
	if err != nil {
		a.Flash().Err(err)
		return
	}
	contextName := a.Config.ActiveContextName()
	v := newProbeView(a, contextName, scope)
	v.transport = transport
	v.loadPlan = func(ctx context.Context) (*probe.Plan, error) {
		reader, err := connection.DynDial()
		if err != nil {
			return nil, errors.New("Kubernetes resource client is unavailable")
		}
		plan, err := probe.Resolve(ctx, reader, contextName, scope)
		if err != nil {
			return nil, err
		}
		if typed, err := connection.Dial(); err == nil && typed != nil {
			plan.Access, plan.AccessReason = probe.CheckAccess(ctx, typed.AuthorizationV1().SelfSubjectAccessReviews(), scope.Namespace)
		}
		return plan, nil
	}
	if err := a.inject(v, false); err != nil {
		a.Flash().Err(err)
	}
}

func newProbeView(app *App, contextName string, scope probe.Scope) *probeView {
	v := &probeView{
		Details:     NewDetails(app, probeTitle, scope.Label(), contentTXT, false),
		contextName: contextName, revision: app.Config.DestinationRevision(), scope: scope,
		attempts: probe.DefaultAttempts, pause: probe.Pause,
	}
	v.slowPresent = func() bool { return probeSiblingPresent(app) }
	return v
}

// probeSiblingPresent says whether the :slow command is registered. The
// probe has no compile-time knowledge of that view: it looks the command up
// among the typed commands, and when the build has none the key is refused.
func probeSiblingPresent(app *App) bool {
	return app != nil && app.command != nil && slices.Contains(app.command.suggestionAliases(), probeSlowCommand)
}

func probeAvailability(target SelectedResourceTarget) string { //nolint:gocritic // Immutable selection.
	if _, err := probeScope(target); err != nil {
		return err.Error()
	}
	return ""
}

func probeActions(owner actionOwner, app *App) []ui.ActionDescriptor {
	target := actionTarget(owner, app.Config.ActiveContextName())
	return []ui.ActionDescriptor{{
		ID: probeActionID, Label: "Probe each Pod: timed GET (asks first)", Category: ui.ActionInspect,
		Shortcut: ":" + probeCommandToken, Discoverable: true, RequiresSelection: true, UnavailableReason: probeAvailability(target),
		Handler: func(*tcell.EventKey) *tcell.EventKey { app.openProbe(target); return nil },
	}}
}

// probeKeyAction is the list key on Service, workload and Pod rows. It
// opens the view; it sends nothing.
func probeKeyAction(app *App, owner actionOwner) ui.KeyAction {
	return ui.NewKeyActionWithOpts("Probe pods", func(*tcell.EventKey) *tcell.EventKey {
		app.openProbe(actionTarget(owner, app.Config.ActiveContextName()))
		return nil
	}, ui.ActionOpts{Visible: true, RequiresSelection: true})
}

func (*probeView) CompactWorkspace() bool { return true }

// selectedTarget is the plan target under the table cursor.
func (v *probeView) selectedTarget() *probe.Target {
	if v.plan == nil {
		return nil
	}
	for i := range v.plan.Targets {
		if v.plan.Targets[i].Pod.Name == v.selected {
			return &v.plan.Targets[i]
		}
	}
	return nil
}

// moveSelection moves the cursor by step Pods and stops at either end.
func (v *probeView) moveSelection(step int) {
	if v.plan == nil || len(v.plan.Targets) == 0 {
		return
	}
	index := 0
	for i := range v.plan.Targets {
		if v.plan.Targets[i].Pod.Name == v.selected {
			index = i
		}
	}
	index = min(max(index+step, 0), len(v.plan.Targets)-1)
	if name := v.plan.Targets[index].Pod.Name; name != v.selected {
		v.selected = name
		v.render()
	}
}

// SelectedResource exposes the Pod under the cursor to shared actions, so
// that a graph or a timeline opened from here follows that Pod. Before a
// plan is read it is the selection the view was opened for.
func (v *probeView) SelectedResource() SelectedResourceTarget {
	if target := v.selectedTarget(); target != nil && !v.handoff {
		return SelectedResourceTarget{Context: v.contextName, GVR: client.PodGVR, Namespace: target.Pod.Namespace,
			Name: target.Pod.Name, UID: types.UID(target.Pod.UID)}
	}
	for gvr, kind := range probeKinds {
		if kind == v.scope.Kind {
			return SelectedResourceTarget{Context: v.contextName, GVR: client.NewGVR(gvr), Namespace: v.scope.Namespace,
				Name: v.scope.Name, UID: types.UID(v.scope.UID)}
		}
	}
	return SelectedResourceTarget{Context: v.contextName, UnavailableReason: "No Pod is selected"}
}

func (v *probeView) Init(ctx context.Context) error {
	if err := v.Details.Init(ctx); err != nil {
		return err
	}
	v.SetBorder(false).SetBorderPadding(0, 0, 0, 0)
	v.app.Styles.RemoveListener(v.Details)
	v.app.Styles.AddListener(v)
	v.header = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.detail = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.footer = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	// The rows are drawn as text, not as a table widget: a table forces
	// one foreground on its selected row, and the selected Pod is often
	// the outlier whose colors matter.
	v.body = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.headerHeight, v.detailHeight = 2, 4
	v.Flex.Clear().SetDirection(tview.FlexRow).
		AddItem(v.header, v.headerHeight, 0, false).
		AddItem(v.body, 0, 1, true).
		AddItem(v.detail, v.detailHeight, 0, false).
		AddItem(v.footer, 1, 0, false)
	v.bindProbeKeys()
	v.render()
	return nil
}

func (v *probeView) bindProbeKeys() {
	v.actions.Delete(ui.KeyC, ui.KeyF, ui.KeyN, ui.KeyShiftN, ui.KeySlash, tcell.KeyCtrlS, tcell.KeyDelete)
	do := func(run func()) ui.ActionHandler {
		return func(*tcell.EventKey) *tcell.EventKey {
			run()
			return nil
		}
	}
	runAction := ui.NewKeyAction("Run probe (asks first)", do(v.askRun), true)
	runAction.Availability = v.runUnavailable
	v.actions.Add(ui.KeyR, runAction)
	// The count is chosen with the digits, as windows are in :graph and
	// :impact; "-" belongs to the application (last view).
	for index, count := range probe.AttemptChoices {
		v.actions.Add(ui.Key1+tcell.Key(index), ui.NewKeyAction(probePlural(count, "request", "requests")+" per pod",
			do(func() { v.setAttempts(count) }), true))
	}
	kindAction := ui.NewKeyAction("Other declared probe", do(v.nextKind), true)
	kindAction.Availability = func() string {
		if v.plan == nil || len(v.plan.Available) < 2 {
			return "These Pods declare one kind of HTTP probe"
		}
		return ""
	}
	v.actions.Add(ui.KeyU, kindAction)
	for key, step := range map[tcell.Key]int{tcell.KeyUp: -1, ui.KeyK: -1, tcell.KeyDown: 1, ui.KeyJ: 1,
		tcell.KeyHome: -probe.MaxPods, tcell.KeyEnd: probe.MaxPods, tcell.KeyPgUp: -4, tcell.KeyPgDn: 4} {
		v.actions.Add(key, ui.NewKeyAction("Select pod", do(func() { v.moveSelection(step) }), false))
	}
	v.actions.Add(tcell.KeyEnter, ui.NewKeyAction("Open pod", do(v.openPod), true))
	v.actions.Add(ui.KeyM, ui.NewKeyAction("Graph pod", do(v.openGraph), true))
	v.actions.Add(ui.KeyT, ui.NewKeyAction("Timeline of pod", do(v.openTimeline), true))
	slow := ui.NewKeyAction("Slow requests: which hop", do(v.openSlow), false)
	slow.Availability = func() string {
		if !v.slowPresent() {
			return "This build has no :slow view"
		}
		return ""
	}
	v.actions.Add(probeSlowKey, slow)
	back := func(event *tcell.EventKey) *tcell.EventKey {
		// Esc stops a running probe first; a second Esc leaves.
		if v.running {
			v.cancelRun()
			return nil
		}
		return v.app.PrevCmd(event)
	}
	v.actions.Add(tcell.KeyEscape, ui.NewKeyAction("Back", back, false))
	v.actions.Add(ui.KeyQ, ui.NewKeyAction("Back", back, false))
}

func (v *probeView) Start() {
	v.active = true
	v.app.Styles.RemoveListener(v.Details)
	v.app.Styles.RemoveListener(v)
	v.app.Styles.AddListener(v)
	v.app.Prompt().SetModel(v.cmdBuff)
	v.StylesChanged(v.app.Styles)
	if v.plan == nil && v.planErr == "" && !v.loading && v.loadPlan != nil {
		v.readPlan()
	}
}

// Stop leaves the view. A probe in flight is canceled: nothing keeps
// sending requests for a view nobody is looking at.
func (v *probeView) Stop() {
	if v.running && v.run != nil {
		v.run.Stopped = "canceled"
	}
	if v.confirm != nil {
		v.confirm = nil
		v.app.Content.Pages.RemovePage(probeConfirmPage)
	}
	v.active, v.loading, v.running = false, false, false
	v.generation++
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	v.app.Styles.RemoveListener(v)
	v.Details.Stop()
}

func (v *probeView) StylesChanged(styles *config.Styles) {
	v.applyStyles(styles)
	if v.body != nil {
		p := styles.Semantic()
		for _, item := range []*tview.TextView{v.header, v.body, v.detail, v.footer} {
			item.SetBackgroundColor(p.Canvas.Color())
			item.SetTextColor(p.Text.Color())
		}
	}
	v.render()
}

func (v *probeView) Draw(screen tcell.Screen) {
	if ui.DrawTaskSizeNotice(screen, v.Flex.Box) {
		return
	}
	_, _, width, height := v.GetInnerRect()
	if width != v.width || height != v.height {
		v.width, v.height = width, height
		v.render()
	}
	v.Flex.Draw(screen)
}

func (v *probeView) destinationCurrent() bool {
	return v.contextName == v.app.Config.ActiveContextName() && v.revision == v.app.Config.DestinationRevision()
}

// readPlan reads the Pods and their declared probes in the background. It
// contacts no Pod.
func (v *probeView) readPlan() {
	if !v.destinationCurrent() || v.loadPlan == nil {
		v.render()
		return
	}
	if v.cancel != nil {
		v.cancel()
	}
	v.generation++
	generation := v.generation
	ctx, cancel := context.WithTimeout(context.Background(), probe.ResolveDeadline)
	v.cancel, v.loading = cancel, true
	v.render()
	go func() {
		defer cancel()
		plan, err := v.loadPlan(ctx)
		if errors.Is(ctx.Err(), context.Canceled) || !v.app.IsRunning() {
			return
		}
		v.app.QueueUpdateDraw(func() {
			if v.active && v.generation == generation && v.destinationCurrent() && v.app.Content.Top() == v {
				v.acceptPlan(plan, err)
			}
		})
	}()
}

func (v *probeView) acceptPlan(plan *probe.Plan, err error) {
	v.loading = false
	if err != nil || plan == nil {
		v.planErr = "the Pods could not be read"
		if err != nil {
			v.planErr = err.Error()
		}
		v.render()
		return
	}
	v.plan, v.planErr = plan, ""
	if v.selectedTarget() == nil && len(plan.Targets) > 0 {
		v.selected = plan.Targets[0].Pod.Name
	}
	v.render()
}

// runUnavailable says why r would send nothing, or "" when it can run.
func (v *probeView) runUnavailable() string {
	switch {
	case !v.destinationCurrent():
		return "Context changed; reopen :probe for the current context"
	case v.running:
		return "A probe is running; Esc cancels it"
	case v.loading || v.plan == nil && v.planErr == "":
		return "The Pods are still being read"
	case v.plan == nil:
		return "The Pods could not be read: " + v.planErr
	case v.plan.Sendable() == 0:
		return "No listed Pod declares an httpGet readiness or liveness probe, so there is nothing this command may send"
	case v.plan.Access == probe.AccessDenied:
		return "Not allowed: " + v.plan.AccessReason
	case v.transport == nil:
		return "No Kubernetes connection"
	}
	return ""
}

// confirmText states exactly what a run sends, before anything is sent.
// The first two paragraphs carry what must be read: the request, how often
// and to how many Pods, and that the application will see it.
func (v *probeView) confirmText() string {
	plan := v.plan
	pods, attempts := plan.Sendable(), v.attempts
	requests, counts := plan.Requests()
	total := probePlural(pods*attempts, "request", "requests")
	var b strings.Builder
	switch {
	case len(requests) == 1 && plan.Scope.Kind == probe.KindPod:
		fmt.Fprintf(&b, "Send %s to %s, %s: %s in all.\n", requests[0], plan.Scope.Label(), probePlural(attempts, "time", "times"), total)
	case len(requests) == 1:
		fmt.Fprintf(&b, "Send %s to %s of %s, %s each: %s in all.\n", requests[0], probePlural(pods, "pod", "pods"),
			plan.Scope.Label(), probePlural(attempts, "time", "times"), total)
	default:
		fmt.Fprintf(&b, "Send each of %s of %s its own declared request, %s each: %s in all.\n", probePlural(pods, "pod", "pods"),
			plan.Scope.Label(), probePlural(attempts, "time", "times"), total)
		for i, request := range requests[:min(len(requests), 4)] {
			fmt.Fprintf(&b, "  %s to %s\n", request, probePlural(counts[i], "pod", "pods"))
		}
		if len(requests) > 4 {
			fmt.Fprintf(&b, "  and %d more different requests\n", len(requests)-4)
		}
	}
	b.WriteString("\nThis is an active request to the application. It is a plain GET with no body and no added header, ")
	b.WriteString("but the application handles it and will see it in its logs.\n\n")
	fmt.Fprintf(&b, "Path and port are those of each pod's own %s: nothing here is typed. ", plan.Kind.Field())
	b.WriteString("It goes through the Kubernetes API server pod proxy, one request after another per pod. ")
	fmt.Fprintf(&b, "Each waits at most %s; the response body is discarded. Esc cancels a running probe.", probe.Seconds(probe.DefaultDeadline.Seconds()))
	if skipped := len(plan.Targets) - pods + plan.LeftOut; skipped > 0 {
		fmt.Fprintf(&b, " %s not contacted.", probePlural(skipped, "pod is", "pods are"))
	}
	return b.String()
}

func probePlural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// askRun is r: it runs at once when this exact request shape was already
// confirmed in this view, and asks first otherwise.
func (v *probeView) askRun() {
	if reason := v.runUnavailable(); reason != "" {
		v.app.Flash().Warn(reason)
		return
	}
	signature := v.plan.Signature(v.attempts)
	if signature == v.confirmed {
		v.startRun()
		return
	}
	pages := v.app.Content.Pages
	dismiss := func() {
		v.confirm = nil
		pages.RemovePage(probeConfirmPage)
		v.app.SetFocus(v)
	}
	send := "Send " + probePlural(v.plan.Sendable()*v.attempts, "request", "requests")
	v.confirm = newProbeConfirm(v.app, "Probe: send requests to the application", v.confirmText(), send, func(send bool) {
		dismiss()
		if !send {
			return
		}
		// The plan can have changed while the question was open; an answer
		// covers only what the question described.
		if v.plan == nil || v.plan.Signature(v.attempts) != signature || v.runUnavailable() != "" {
			v.app.Flash().Warn("The request changed while you were asked; press r to see it again")
			return
		}
		v.confirmed = signature
		v.startRun()
	})
	pages.AddPage(probeConfirmPage, v.confirm, false, true)
	v.app.SetFocus(v.confirm)
}

// startRun sends the confirmed requests in the background. Every answer is
// handed to the UI goroutine; a canceled or superseded run reports nothing.
func (v *probeView) startRun() {
	if v.cancel != nil {
		v.cancel()
	}
	v.generation++
	generation := v.generation
	plan := *v.plan
	plan.Targets = append([]probe.Target(nil), v.plan.Targets...)
	options := probe.Options{Attempts: v.attempts, Pause: v.pause}
	if v.run != nil && v.run.Sent() > 0 {
		v.previous, v.previousAt = v.run, v.ranAt
	}
	v.run = probe.NewRun(&plan, options)
	v.ranAt = time.Now().UTC()
	v.run.StartedAt = v.ranAt
	v.elapsed = 0
	ctx, cancel := context.WithCancel(context.Background())
	v.cancel, v.running = cancel, true
	transport := v.transport
	v.render()
	go func() {
		defer cancel()
		current := func() bool {
			return v.active && v.generation == generation && v.destinationCurrent() && v.app.Content.Top() == v
		}
		done := probe.Execute(ctx, transport, &plan, options, func(pod, attempt int, result probe.Attempt) {
			if !v.app.IsRunning() {
				return
			}
			v.app.QueueUpdateDraw(func() {
				if current() && v.run != nil && pod < len(v.run.Pods) && attempt < len(v.run.Pods[pod].Attempts) {
					v.run.Pods[pod].Attempts[attempt] = result
					v.elapsed = time.Since(v.ranAt)
					v.render()
				}
			})
		})
		if !v.app.IsRunning() {
			return
		}
		v.app.QueueUpdateDraw(func() {
			if current() {
				v.finish(done)
			}
		})
	}()
}

// finish accepts a completed run and moves the cursor to the first Pod
// that differs from its peers, so the pane starts on the evidence.
func (v *probeView) finish(done *probe.Run) {
	v.run, v.running = done, false
	v.elapsed = done.EndsAt.Sub(done.StartedAt)
	summary := probe.Summarize(v.plan, v.run, nil)
	for i := range summary.Pods {
		if summary.Pods[i].Verdict.Flagged() {
			v.selected = v.plan.Targets[i].Pod.Name
			break
		}
	}
	v.render()
}

// cancelRun stops a running probe. Requests in flight are abandoned and
// what was not sent stays unsent; the answers so far remain on screen.
func (v *probeView) cancelRun() {
	if !v.running {
		return
	}
	v.generation++
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	v.running = false
	if v.run != nil {
		v.run.Stopped = "canceled"
		v.run.EndsAt = time.Now().UTC()
		v.elapsed = v.run.EndsAt.Sub(v.run.StartedAt)
	}
	v.app.Flash().Info("Probe canceled; no further request is sent")
	v.render()
}

// setAttempts changes how many requests the next run sends to each Pod. A
// run in flight keeps the count it was confirmed with.
func (v *probeView) setAttempts(count int) {
	if v.running {
		v.app.Flash().Warn("A probe is running; Esc cancels it")
		return
	}
	v.attempts = probe.ClampAttempts(count)
	v.render()
}

// nextKind switches between the declared readiness and liveness probes.
// Answers to another path are not comparable, so earlier runs are dropped.
func (v *probeView) nextKind() {
	switch {
	case v.running:
		v.app.Flash().Warn("A probe is running; Esc cancels it")
	case v.plan == nil || len(v.plan.Available) < 2:
		v.app.Flash().Warn("These Pods declare one kind of HTTP probe; the path is never typed")
	default:
		v.plan.Select(v.plan.NextKind())
		v.run, v.previous = nil, nil
		v.render()
	}
}

// openPod opens the Pod list on the selected Pod, the way related-resource
// jumps do.
func (v *probeView) openPod() {
	target := v.selectedTarget()
	if target == nil {
		v.app.Flash().Warn("No Pod is selected")
		return
	}
	if !v.destinationCurrent() {
		v.app.Flash().Warn("Context changed; reopen :probe")
		return
	}
	selection := v.SelectedResource()
	v.app.gotoResource(client.PodGVR.String()+" "+target.Pod.Namespace, selection.Path(), false, true)
	if native, ok := v.app.Content.Top().(ResourceViewer); ok && native.GetTable() != nil && native.GVR() == client.PodGVR {
		native.GetTable().expectedTarget = &selection
	}
}

func (v *probeView) openGraph() {
	if v.selectedTarget() == nil {
		v.app.Flash().Warn("No Pod is selected")
		return
	}
	if !v.destinationCurrent() {
		v.app.Flash().Warn("Context changed; reopen :probe")
		return
	}
	v.app.openGraph(v.SelectedResource(), 0)
}

func (v *probeView) openTimeline() {
	if !v.destinationCurrent() {
		v.app.Flash().Warn("Context changed; reopen :probe")
		return
	}
	v.app.openTimeline(v.SelectedResource(), "")
}

// openSlow hands the selection the probe was opened for to :slow.
func (v *probeView) openSlow() {
	if !v.slowPresent() {
		v.app.Flash().Warn("This build has no :slow view")
		return
	}
	if !v.destinationCurrent() {
		v.app.Flash().Warn("Context changed; reopen :probe")
		return
	}
	v.handoff = true
	defer func() { v.handoff = false }()
	v.app.gotoResource(probeSlowCommand, "", false, true)
}
