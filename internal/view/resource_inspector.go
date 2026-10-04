// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
)

const (
	tlsCommand          = "tls"
	troubleshootCommand = "troubleshoot"
)

type inspectionSnapshot struct {
	Text       string
	UID        types.UID
	CapturedAt time.Time
}

// inspectionDetails cancels on exit and never updates a replaced screen.
type inspectionDetails struct {
	*Details
	cancel          context.CancelFunc
	generation      uint64
	contextName     string
	secretPath      string
	target          SelectedResourceTarget
	connection      client.Connection
	snapshot        inspectionSnapshot
	displayEvidence string
	messagesCompact bool
	loader          func(context.Context) (string, error)
	snapshotLoader  func(context.Context, SelectedResourceTarget) (inspectionSnapshot, error)
	related         func(context.Context, SelectedResourceTarget) ([]inspectionReference, error)
}

func (d *inspectionDetails) SelectedResource() SelectedResourceTarget { return d.target }

func (d *inspectionDetails) Stop() {
	d.generation++
	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
	d.Details.Stop()
}

func (c *Command) investigationCommand(name string) {
	v, ok := c.app.Content.Top().(ResourceViewer)
	if !ok {
		c.app.Flash().Err(fmt.Errorf("open a resource list first"))
		return
	}
	target := resolveSelectedResource(v, c.app.Config.ActiveContextName())
	if name == actionsCommand {
		p := &actionPalette{Picker: NewPicker(), owner: v, app: c.app, path: target.Path(), target: target}
		if err := c.app.inject(p, false); err != nil {
			c.app.Flash().Err(err)
		}
		return
	}
	c.app.openTargetInspection(target, name)
}
func (a *App) openInspection(v ResourceViewer, name, path string) {
	a.openTargetInspection(selectedResourceForPath(v, a.Config.ActiveContextName(), path), name)
}

func (a *App) openTargetInspection(target SelectedResourceTarget, name string) {
	if err := target.Err(); err != nil {
		a.Flash().Err(err)
		return
	}
	if target.Context != a.Config.ActiveContextName() {
		a.Flash().Warn("Context changed; reopen inspection")
		return
	}
	d := &inspectionDetails{Details: NewDetails(a, name, target.Path(), contentInspection, true).Update("Loading read-only snapshot..."), target: target}
	if name == tlsCommand && target.GVR.R() == "secrets" {
		d.secretPath = target.Path()
	}
	connection, err := pinInspectionConnection(a.Conn())
	if err != nil {
		a.Flash().Err(err)
		return
	}
	d.connection = connection
	d.snapshotLoader = func(ctx context.Context, target SelectedResourceTarget) (inspectionSnapshot, error) {
		return loadTargetInspectionSnapshot(ctx, connection, target, name)
	}
	d.related = func(ctx context.Context, target SelectedResourceTarget) ([]inspectionReference, error) {
		return loadTargetInspectionReferences(ctx, connection, target, name)
	}
	if err := a.inject(d, false); err != nil {
		a.Flash().Err(err)
		return
	}
	d.refresh()
}

func (d *inspectionDetails) Init(ctx context.Context) error {
	d.contextName = d.app.Config.ActiveContextName()
	if err := d.Details.Init(ctx); err != nil {
		return err
	}
	d.actions.Add(ui.KeyR, ui.NewKeyAction("Refresh snapshot", func(*tcell.EventKey) *tcell.EventKey { d.refresh(); return nil }, true))
	d.actions.Add(ui.KeyM, ui.NewKeyAction("Toggle full messages", func(*tcell.EventKey) *tcell.EventKey {
		d.messagesCompact = !d.messagesCompact
		d.renderSnapshotText(d.displayEvidence)
		return nil
	}, true))
	if d.related != nil {
		d.actions.Add(ui.KeyG, ui.NewKeyAction("Related resources", func(*tcell.EventKey) *tcell.EventKey { d.openRelated(); return nil }, true))
	}
	if d.title == tlsCommand {
		d.actions.Add(ui.KeyP, ui.NewKeyAction("Probe TLS endpoint", func(*tcell.EventKey) *tcell.EventKey { d.tlsForm(true); return nil }, true))
		if d.secretPath != "" {
			d.actions.Add(ui.KeyV, ui.NewKeyAction("Verify certificate trust", func(*tcell.EventKey) *tcell.EventKey { d.tlsForm(false); return nil }, true))
		}
	}
	return nil
}
func (d *inspectionDetails) Start() {
	d.app.Styles.RemoveListener(d.Details)
	d.app.Styles.AddListener(d.Details)
	d.app.Prompt().SetModel(d.cmdBuff)
	if d.snapshot.Text != "" {
		d.app.Flash().Infof("Retained snapshot from %s; r makes a new observation", d.snapshot.CapturedAt.UTC().Format(time.RFC3339))
	}
}
func (d *inspectionDetails) refresh() {
	if d.contextName != d.app.Config.ActiveContextName() {
		d.app.Flash().Warn("Context changed; reopen inspection")
		return
	}
	if d.cancel != nil {
		d.cancel()
	}
	d.generation++
	generation := d.generation
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	d.cancel = cancel
	d.app.Flash().Info("Loading inspection snapshot...")
	target := d.target
	go func() {
		defer cancel()
		var snapshot inspectionSnapshot
		var err error
		if d.snapshotLoader != nil {
			snapshot, err = d.snapshotLoader(ctx, target)
		} else {
			snapshot.Text, err = d.loader(ctx)
			snapshot.CapturedAt = time.Now()
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		d.app.QueueUpdateDraw(func() {
			if d.app.Content.Top() == d && d.generation == generation && d.contextName == d.app.Config.ActiveContextName() {
				d.acceptSnapshot(snapshot, err)
			}
		})
	}()
}

func (d *inspectionDetails) acceptSnapshot(snapshot inspectionSnapshot, err error) {
	text := snapshot.Text
	if err != nil {
		text = "Inspection unavailable: " + err.Error()
		if d.snapshot.Text != "" {
			text += "\n\nRETAINED SNAPSHOT (refresh failed; evidence below was not replaced)\n" + d.snapshot.Text
		}
	} else {
		d.snapshot = snapshot
		if snapshot.UID != "" {
			d.target.UID = snapshot.UID
		}
	}
	d.displayEvidence = text
	d.renderSnapshotText(text)
}

func (d *inspectionDetails) renderSnapshotText(text string) {
	if d.messagesCompact {
		text = compactInspectionMessages(text)
	}
	query, region := d.inspectionQuery, d.currentRegion
	row, col := d.text.GetScrollOffset()
	d.Update(text)
	if query != "" {
		d.model.Filter(query)
		if region < d.maxRegions {
			d.currentRegion = region
			d.text.Highlight(fmt.Sprintf("search_%d", region))
		}
	}
	d.text.ScrollTo(row, col)
	d.updateTitle()
}

func loadInspection(ctx context.Context, conn client.Connection, gvr *client.GVR, path, name string) (string, error) {
	return loadTargetInspection(ctx, conn, resourceTargetForPath(gvr, "", path), name)
}

func loadTargetInspection(ctx context.Context, conn client.Connection, target SelectedResourceTarget, name string) (string, error) {
	snapshot, err := loadTargetInspectionSnapshot(ctx, conn, target, name)
	return snapshot.Text, err
}

func loadTargetInspectionSnapshot(ctx context.Context, conn client.Connection, target SelectedResourceTarget, name string) (inspectionSnapshot, error) {
	if err := target.Err(); err != nil {
		return inspectionSnapshot{}, err
	}
	dyn, err := conn.DynDial()
	if err != nil {
		return inspectionSnapshot{}, err
	}
	obj, err := dyn.Resource(target.GVR.GVR()).Namespace(target.Namespace).Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return inspectionSnapshot{}, err
	}
	if err := verifySelectedIdentity(target, obj); err != nil {
		return inspectionSnapshot{}, err
	}
	snapshot := inspectionSnapshot{UID: obj.GetUID(), CapturedAt: time.Now()}
	identity := ""
	if target.UID == "" {
		identity = "\nSelected UID: unknown; continuity with the selected row cannot be verified.\n"
	}
	if name == tlsCommand {
		text, err := tlsResourceReport(ctx, conn, obj)
		snapshot.Text = text + identity
		return snapshot, err
	}
	snapshot.Text = resourceSummaryAt(obj, snapshot.CapturedAt) + identity
	snapshot.Text += workloadDiagnostics(ctx, conn, obj)
	snapshot.Text += resourceEvents(ctx, conn, obj)
	snapshot.Text += resourceOwners(obj)
	return snapshot, nil
}

func resourceEvents(ctx context.Context, conn client.Connection, obj *unstructured.Unstructured) string {
	if obj.GetUID() == "" {
		return "\nEvents unavailable: object UID missing\n"
	}
	k, err := conn.Dial()
	if err != nil {
		return "\nEvents unavailable: " + err.Error() + "\n"
	}
	events, err := k.CoreV1().Events(obj.GetNamespace()).List(ctx, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("involvedObject.uid", string(obj.GetUID())).String(), Limit: 100,
	})
	if err != nil {
		return "\nEvents unavailable: " + err.Error() + "\n"
	}
	sort.SliceStable(events.Items, func(i, j int) bool { return eventTime(&events.Items[i]).After(eventTime(&events.Items[j])) })
	text := "\nEVENTS (UID-scoped API snapshot; latest first, not a complete history)\n"
	for i := range events.Items {
		e := &events.Items[i]
		text += fmt.Sprintf("%s  %s  %s  count=%d\n", eventTime(e).UTC().Format(time.RFC3339), e.Type, e.Reason, e.Count)
		text += inspectionMessage(e.Message)
	}
	if len(events.Items) == 0 {
		text += "No retained events reported. This does not establish health.\n"
	}
	if events.Continue != "" {
		text += "Event results truncated at 100; use the Events view for more.\n"
	}
	return text
}

func resourceSummary(o *unstructured.Unstructured) string {
	return resourceSummaryAt(o, time.Now())
}

func resourceSummaryAt(o *unstructured.Unstructured, captured time.Time) string {
	var b strings.Builder
	uid := string(o.GetUID())
	if uid == "" {
		uid = "unknown"
	}
	fmt.Fprintf(&b, "READ-ONLY SNAPSHOT\n%s %s\nUID: %s\nCaptured: %s\nSource: Kubernetes API; bounded observation, not a health verdict\n",
		o.GetKind(), client.FQN(o.GetNamespace(), o.GetName()), uid, captured.UTC().Format(time.RFC3339))
	b.WriteString(resourceStatus(o))
	b.WriteString("\nCONDITIONS\n")
	conditions, _, _ := unstructured.NestedSlice(o.Object, "status", "conditions")
	sort.SliceStable(conditions, func(i, j int) bool { return conditionPriority(conditions[i]) < conditionPriority(conditions[j]) })
	for _, c := range conditions {
		if m, ok := c.(map[string]any); ok {
			fmt.Fprintf(&b, "%v: %v", m["type"], m["status"])
			if reason, ok := m["reason"].(string); ok && reason != "" {
				fmt.Fprintf(&b, " | %s", reason)
			}
			b.WriteString("\n")
			if message, ok := m["message"].(string); ok && message != "" {
				b.WriteString(inspectionMessage(message))
			}
		}
	}
	if len(conditions) == 0 {
		b.WriteString("No conditions reported; absence is not proof of health.\n")
	}
	b.WriteString("\nCONTAINERS (selected resource only)\n")
	for _, field := range []string{"initContainerStatuses", "containerStatuses", "ephemeralContainerStatuses"} {
		containers, _, _ := unstructured.NestedSlice(o.Object, "status", field)
		for _, c := range containers {
			if m, ok := c.(map[string]any); ok {
				fmt.Fprintf(&b, "%v: %s | restarts: %v\n", m["name"], containerReadiness(m["ready"]), m["restartCount"])
				for _, state := range []string{"state", "lastState"} {
					for _, phase := range []string{"waiting", "running", "terminated"} {
						s, found, _ := unstructured.NestedMap(m, state, phase)
						if found {
							fmt.Fprintf(&b, "  %s: %s", state, phase)
							for _, key := range []string{"reason", "exitCode", "startedAt", "finishedAt"} {
								if value, ok := s[key]; ok {
									fmt.Fprintf(&b, " %s=%v", key, value)
								}
							}
							b.WriteString("\n")
							if message, ok := s["message"].(string); ok && message != "" {
								b.WriteString(inspectionMessage(message))
							}
						}
					}
				}
			}
		}
	}
	return b.String()
}

func inspectionMessage(message string) string {
	lines := strings.Split(message, "\n")
	if len(lines) == 0 || message == "" {
		return ""
	}
	text := "  Message: " + lines[0] + "\n"
	for _, line := range lines[1:] {
		text += "    Message continuation: " + line + "\n"
	}
	return text
}

func compactInspectionMessages(text string) string {
	lines := strings.Split(text, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(line, "    Message continuation: ") {
			continue
		}
		if strings.HasPrefix(line, "  Message: ") {
			runes := []rune(line)
			if len(runes) > 120 {
				line = string(runes[:120]) + "…"
			}
			line += " (m expands full messages)"
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}

func conditionPriority(raw any) int {
	condition, ok := raw.(map[string]any)
	if !ok {
		return 3
	}
	if condition["status"] == "Unknown" || condition["status"] == "False" {
		return 0
	}
	if condition["status"] == "True" && (condition["type"] == "Failed" || condition["type"] == "Degraded") {
		return 0
	}
	return 1
}

func containerReadiness(value any) string {
	ready, reported := value.(bool)
	if !reported {
		return "Readiness unknown"
	}
	if ready {
		return "Ready"
	}
	return "Not ready"
}

func resourceOwners(o *unstructured.Unstructured) string {
	var b strings.Builder
	b.WriteString("\nOWNERS\n")
	for _, r := range o.GetOwnerReferences() {
		fmt.Fprintf(&b, "%s %s (%s)\n", r.Kind, r.Name, r.APIVersion)
	}
	if len(o.GetOwnerReferences()) == 0 {
		b.WriteString("No owner references reported.\n")
	}
	return b.String()
}

func resourceStatus(o *unstructured.Unstructured) string {
	var b strings.Builder
	b.WriteString("\nSTATUS / REASON\n")
	for _, field := range []string{"phase", "reason", "message"} {
		if value := nestedText(o.Object, "status", field); value != "" {
			fmt.Fprintf(&b, "%s: %s\n", field, value)
		}
	}
	for _, field := range []string{"initContainerStatuses", "containerStatuses", "ephemeralContainerStatuses"} {
		statuses, _, _ := unstructured.NestedSlice(o.Object, "status", field)
		for _, row := range statuses {
			status, ok := row.(map[string]any)
			if !ok {
				continue
			}
			for _, state := range []string{"state", "lastState"} {
				for _, phase := range []string{"waiting", "terminated"} {
					if reason := nestedText(status, state, phase, "reason"); reason != "" {
						fmt.Fprintf(&b, "Container %v | %s %s: %s | restarts: %v\n", status["name"], state, phase, reason, status["restartCount"])
					}
				}
			}
		}
	}
	if b.String() == "\nSTATUS / REASON\n" {
		b.WriteString("No phase or failure reason reported; inspect the conditions and evidence below.\n")
	}
	return b.String()
}

func inspectionMarkup(a *App, text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		switch {
		case line == "TRUST SOURCE" || line == "VERIFIED TLS HANDSHAKE" || line == "TLS CONFIGURATION REFERENCES" || strings.HasPrefix(line, "WORKLOAD PODS (") ||
			line == "OWNERS" || line == "CONDITIONS" || line == "STATUS / REASON" || line == "READ-ONLY SNAPSHOT" ||
			strings.HasPrefix(line, "CONTAINERS (") || strings.HasPrefix(line, "EVENTS (") || strings.HasPrefix(line, "CERTIFICATE "):
			lines[i] = detailStyled(a.Styles.Views().Yaml.KeyColor.String(), "b", line)
		case line == "EXPIRED" || line == "NOT YET VALID" || line == "VERIFICATION FAILED":
			lines[i] = detailStyled(a.Styles.K9s.Frame.Status.ErrorColor.String(), "b", line)
		default:
			lines[i] = wbText(line)
		}
	}
	return strings.Join(lines, "\n")
}

func eventTime(e *corev1.Event) time.Time {
	if e.Series != nil && !e.Series.LastObservedTime.IsZero() {
		return e.Series.LastObservedTime.Time
	}
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	if !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	return e.CreationTimestamp.Time
}
