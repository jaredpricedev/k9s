// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/fleet"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/mattn/go-runewidth"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

const fleetCommandToken = "fleet"

type fleetWorkspace struct {
	*Details
	target                          SelectedResourceTarget
	scope                           fleet.Scope
	snapshot                        *fleet.Snapshot
	loader                          func(context.Context, fleet.Scope) (*fleet.Snapshot, error)
	cancel                          context.CancelFunc
	generation, destinationRevision uint64
	active, loading                 bool
	failure                         string
	header, footer                  *tview.TextView
	width, height                   int
}

func (c *Command) fleetCommand(line string) {
	fields := strings.Fields(line)
	if len(fields) != 2 && len(fields) != 5 {
		c.app.Flash().Warn("Use :fleet peer-context [deployments|statefulsets|daemonsets|jobs namespace name]")
		return
	}
	var target SelectedResourceTarget
	if len(fields) == 2 {
		owner, ok := c.app.Content.Top().(actionOwner)
		if !ok {
			c.app.Flash().Warn("Select a native workload first")
			return
		}
		target = actionTarget(owner, c.app.Config.ActiveContextName())
		if target.UID == "" {
			c.app.Flash().Warn("Resource UID unavailable; refresh the source list")
			return
		}
	} else {
		kinds := map[string]*client.GVR{"deployments": client.DpGVR, "statefulsets": client.StsGVR, "daemonsets": client.DsGVR, "jobs": client.JobGVR}
		target = SelectedResourceTarget{Context: c.app.Config.ActiveContextName(), GVR: kinds[fields[2]], Namespace: fields[3], Name: fields[4]}
	}
	if err := target.Err(); err != nil {
		c.app.Flash().Warn(err.Error())
		return
	}
	scope := fleet.Scope{Contexts: [2]string{target.Context, fields[1]}, GVR: target.GVR.GVR(), Namespace: target.Namespace, Name: target.Name, PrimaryUID: target.UID}
	if err := scope.Validate(); err != nil {
		c.app.Flash().Warn(err.Error())
		return
	}
	conn := c.app.Conn()
	if conn == nil || conn.Config() == nil {
		c.app.Flash().Warn("Context configuration unavailable")
		return
	}
	factory := fleetActorFactory(conn.Config(), &scope)
	v := &fleetWorkspace{Details: NewDetails(c.app, "Fleet facts", target.Path(), contentInspection, true),
		target: target, scope: scope, destinationRevision: c.app.Config.DestinationRevision()}
	v.loader = func(ctx context.Context, scope fleet.Scope) (*fleet.Snapshot, error) {
		return fleet.Collect(ctx, scope, factory)
	}
	if err := c.app.inject(v, false); err != nil {
		c.app.Flash().Err(err)
	}
}

// fleetActorFactory captures both actor configurations without global switching.
func fleetActorFactory(cfg *client.Config, scope *fleet.Scope) fleet.Factory {
	// Capture both configurations on the UI thread, without contacting either API.
	snapshots := [2]*client.Config{cfg.Snapshot(scope.Contexts[0]), cfg.Snapshot(scope.Contexts[1])}
	return func(ctx context.Context, name string) (fleet.Actor, error) {
		index := 0
		if name == scope.Contexts[1] {
			index = 1
		}
		restConfig, err := snapshots[index].RESTConfig()
		if err != nil {
			return fleet.Actor{}, err
		}
		if cancelErr := ctx.Err(); cancelErr != nil {
			return fleet.Actor{}, cancelErr
		}
		restConfig.Timeout = fleet.ReadTimeout
		reader, err := dynamic.NewForConfig(restConfig)
		if err != nil {
			return fleet.Actor{}, err
		}
		core, err := typedcorev1.NewForConfig(restConfig)
		if err != nil {
			return fleet.Actor{}, err
		}
		return fleet.Actor{Reader: reader, Authority: restConfig.Host, NamespaceGet: func(ctx context.Context, name string) (*corev1.Namespace, error) {
			return core.Namespaces().Get(ctx, name, metav1.GetOptions{})
		}}, nil
	}
}

func (*fleetWorkspace) CompactWorkspace() bool                     { return true }
func (v *fleetWorkspace) SelectedResource() SelectedResourceTarget { return v.target }
func (v *fleetWorkspace) Init(ctx context.Context) error {
	if err := v.Details.Init(ctx); err != nil {
		return err
	}
	v.SetBorder(false).SetBorderPadding(0, 0, 0, 0)
	v.app.Styles.RemoveListener(v.Details)
	v.app.Styles.AddListener(v)
	v.header = tview.NewTextView().SetWrap(false)
	v.footer = tview.NewTextView().SetWrap(false)
	v.Flex.Clear().SetDirection(tview.FlexRow).AddItem(v.header, 3, 0, false).AddItem(v.text, 0, 1, true).AddItem(v.footer, 1, 0, false)
	v.actions.Add(ui.KeyR, ui.NewKeyAction("Refresh two-context facts", func(event *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return event
		}
		v.refresh()
		return nil
	}, true))
	v.render()
	return nil
}
func (v *fleetWorkspace) Start() {
	v.active = true
	v.app.Prompt().SetModel(v.cmdBuff)
	v.app.Styles.RemoveListener(v.Details)
	v.app.Styles.RemoveListener(v)
	v.app.Styles.AddListener(v)
	v.StylesChanged(v.app.Styles)
	if v.snapshot == nil && !v.loading && v.loader != nil {
		v.refresh()
	}
}
func (v *fleetWorkspace) Stop() {
	v.active, v.loading = false, false
	v.generation++
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	v.app.Styles.RemoveListener(v)
	v.Details.Stop()
}
func (v *fleetWorkspace) StylesChanged(styles *config.Styles) { v.applyStyles(styles); v.render() }
func (v *fleetWorkspace) Draw(screen tcell.Screen) {
	if ui.DrawTaskSizeNotice(screen, v.Flex.Box) {
		return
	}
	_, _, width, height := v.GetInnerRect()
	if width != v.width || height != v.height {
		v.width, v.height = width, height
		v.render()
	}
	v.chrome()
	v.Flex.Draw(screen)
}
func (v *fleetWorkspace) destinationCurrent() bool {
	return v.target.Context == v.app.Config.ActiveContextName() && v.destinationRevision == v.app.Config.DestinationRevision()
}
func (v *fleetWorkspace) refresh() {
	if !v.destinationCurrent() {
		v.failure = "Destination changed; reopen fleet facts"
		v.render()
		return
	}
	if v.loader == nil {
		return
	}
	if v.cancel != nil {
		v.cancel()
	}
	v.generation++
	generation, scope := v.generation, v.scope
	ctx, cancel := context.WithTimeout(v.app.sessionContext(), fleet.CollectionTimeout)
	v.cancel, v.loading = cancel, true
	v.chrome()
	go func() {
		defer cancel()
		snapshot, err := v.loader(ctx, scope)
		if ctx.Err() == context.Canceled || !v.app.IsRunning() {
			return
		}
		v.app.QueueUpdateDraw(func() {
			if v.accepts(generation) {
				v.accept(snapshot, err)
			}
		})
	}()
}
func (v *fleetWorkspace) accepts(generation uint64) bool {
	return v.active && generation == v.generation && v.destinationCurrent() && v.app.Content.Top() == v
}
func (v *fleetWorkspace) accept(snapshot *fleet.Snapshot, err error) {
	v.loading = false
	if err != nil {
		v.failure = "Collection unavailable; prior facts retained"
	} else if snapshot == nil {
		v.failure = "No observations returned"
	} else {
		v.failure = ""
		if v.snapshot != nil {
			for i := range snapshot.Observations {
				fresh, prior := snapshot.Observations[i], v.snapshot.Observations[i]
				if len(fresh.Facts) == 0 && len(prior.Facts) > 0 && transientFleetFailure(fresh.State) {
					prior.State = "Retained; refresh " + fresh.State
					snapshot.Observations[i] = prior
				}
			}
		}
		v.snapshot = snapshot
		if v.scope.PrimaryUID == "" && len(snapshot.Observations[0].Facts) > 0 {
			v.scope.PrimaryUID = snapshot.Observations[0].UID
			v.target.UID = v.scope.PrimaryUID
		}
	}
	v.render()
}

func transientFleetFailure(state string) bool {
	switch state {
	case inspect.ObservationDenied, "authentication unavailable", "timeout", "canceled", "actor unavailable":
		return true
	default:
		return strings.HasPrefix(state, "read/setup unavailable")
	}
}

func (v *fleetWorkspace) chrome() {
	if v.header == nil {
		return
	}
	width := v.width
	if width <= 0 {
		width = 80
	}
	state := "Read only | named GETs | pending"
	if v.snapshot != nil {
		state = "API facts | independent coverage | " + v.snapshot.CapturedAt.UTC().Format("15:04:05Z")
	}
	if v.loading {
		state = "Refreshing | prior facts retained"
	}
	if v.failure != "" {
		state = v.failure
	}
	if !v.destinationCurrent() {
		state = retainedDestinationChanged
	}
	lines := []string{"Fleet facts / " + v.target.Path(), v.scope.Contexts[0] + " <> " + v.scope.Contexts[1], state}
	for i, line := range lines {
		lines[i] = tview.Escape(ui.Truncate(line, width))
	}
	v.header.SetText(strings.Join(lines, "\n"))
	v.footer.SetText(tview.Escape(ui.Truncate("r refresh  / search  Esc back", width)))
	styles := v.app.Styles.Semantic()
	for _, item := range []*tview.TextView{v.header, v.footer} {
		item.SetBackgroundColor(styles.Panel.Color())
		item.SetTextColor(styles.Text.Color())
	}
}
func (v *fleetWorkspace) render() {
	if v.header == nil {
		return
	}
	v.chrome()
	body := "Loading two explicit named reads..."
	if v.snapshot != nil {
		body = fleetText(v.snapshot, v.width)
	} else if v.failure != "" {
		body = v.failure
	}
	row, col := v.text.GetScrollOffset()
	query := v.inspectionQuery
	v.Update(body)
	if query != "" {
		v.model.Filter(query)
	}
	v.text.ScrollTo(row, col)
}
func fleetText(snapshot *fleet.Snapshot, width int) string {
	panels := struct{ primary, peer []string }{}
	for i := range snapshot.Observations {
		o := &snapshot.Observations[i]
		namespaceUID := string(o.NamespaceUID)
		if namespaceUID == "" {
			namespaceUID = "unknown (optional exact GET)"
		}
		lines := []string{
			o.Context + " / " + o.State, "API " + o.Authority, "Namespace " + o.Namespace,
			"Namespace UID " + namespaceUID, "UID " + string(o.UID),
			"RV " + o.ResourceVersion + " | " + o.CapturedAt.UTC().Format(time.RFC3339),
		}
		for _, fact := range o.Facts {
			lines = append(lines, fact.Category+" / "+fact.Name+": "+fact.Value)
		}
		if i == 0 {
			panels.primary = lines
		} else {
			panels.peer = lines
		}
	}
	var lines []string
	if width >= 100 {
		half := (width - 3) / 2
		count := max(len(panels.primary), len(panels.peer))
		for i := range count {
			left, right := "", ""
			if i < len(panels.primary) {
				left = ui.Truncate(panels.primary[i], half)
			}
			if i < len(panels.peer) {
				right = ui.Truncate(panels.peer[i], half)
			}
			lines = append(lines, left+strings.Repeat(" ", max(0, half-runewidth.StringWidth(left)))+" | "+right)
		}
	} else {
		lines = append(lines, panels.primary...)
		lines = append(lines, "")
		lines = append(lines, panels.peer...)
	}
	if snapshot.MayAlias() {
		lines = append(lines, "WARNING: same authority and Namespace UID; contexts may alias one cluster")
	}
	lines = append(lines,
		"Scope: exact "+snapshot.Scope.GVR.String()+" / "+snapshot.Scope.Namespace+"/"+snapshot.Scope.Name,
		"Names do not establish the same application or cross-cluster identity.",
		"Template images are declarations; no runtime image adoption evidence.",
		"Facts do not prove compatibility or rollout health; missing status is unknown.",
	)
	return strings.Join(lines, "\n")
}
