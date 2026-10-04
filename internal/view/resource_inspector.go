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
)

const (
	tlsCommand          = "tls"
	troubleshootCommand = "troubleshoot"
)

// inspectionDetails cancels on exit and never updates a replaced screen.
type inspectionDetails struct {
	*Details
	cancel      context.CancelFunc
	generation  uint64
	contextName string
	secretPath  string
	target      SelectedResourceTarget
	loader      func(context.Context) (string, error)
	related     func(context.Context) ([]inspectionReference, error)
}

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
	d.loader = func(ctx context.Context) (string, error) { return loadTargetInspection(ctx, connection, target, name) }
	d.related = func(ctx context.Context) ([]inspectionReference, error) {
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
	go func() {
		defer cancel()
		text, err := d.loader(ctx)
		if err != nil {
			text = "Inspection unavailable: " + err.Error()
		}
		d.app.QueueUpdateDraw(func() {
			if d.app.Content.Top() == d && d.generation == generation && d.contextName == d.app.Config.ActiveContextName() {
				d.Update(text)
			}
		})
	}()
}

func loadInspection(ctx context.Context, conn client.Connection, gvr *client.GVR, path, name string) (string, error) {
	return loadTargetInspection(ctx, conn, resourceTargetForPath(gvr, "", path), name)
}

func loadTargetInspection(ctx context.Context, conn client.Connection, target SelectedResourceTarget, name string) (string, error) {
	if err := target.Err(); err != nil {
		return "", err
	}
	dyn, err := conn.DynDial()
	if err != nil {
		return "", err
	}
	obj, err := dyn.Resource(target.GVR.GVR()).Namespace(target.Namespace).Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if err := verifySelectedIdentity(target, obj); err != nil {
		return "", err
	}
	identity := ""
	if target.UID == "" {
		identity = "\nSelected UID: unknown; continuity with the selected row cannot be verified.\n"
	}
	if name == tlsCommand {
		text, err := tlsResourceReport(ctx, conn, obj)
		return text + identity, err
	}
	text := resourceSummary(obj) + identity
	text += workloadDiagnostics(ctx, conn, obj)
	if obj.GetUID() == "" {
		return text + "\nEvents unavailable: object UID missing", nil
	}
	k, err := conn.Dial()
	if err != nil {
		return text + "\nEvents unavailable: " + err.Error(), nil
	}
	events, err := k.CoreV1().Events(target.Namespace).List(ctx, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("involvedObject.uid", string(obj.GetUID())).String(), Limit: 100,
	})
	if err != nil {
		return text + "\nEvents unavailable: " + err.Error(), nil
	}
	sort.SliceStable(events.Items, func(i, j int) bool { return eventTime(&events.Items[i]).After(eventTime(&events.Items[j])) })
	text += "\nEVENTS (API snapshot; not a complete history)\n"
	for i := range events.Items {
		e := &events.Items[i]
		text += fmt.Sprintf("%s  %s  %s  count=%d\n  %s\n", eventTime(e).UTC().Format(time.RFC3339), e.Type, e.Reason, e.Count, e.Message)
	}
	if len(events.Items) == 0 {
		text += "No retained events reported. This does not establish health.\n"
	}
	if events.Continue != "" {
		text += "Event results truncated at 100; use the Events view for more.\n"
	}
	return text, nil
}

func resourceSummary(o *unstructured.Unstructured) string {
	var b strings.Builder
	uid := string(o.GetUID())
	if uid == "" {
		uid = "unknown"
	}
	fmt.Fprintf(&b, "READ-ONLY SNAPSHOT\n%s %s/%s\nUID: %s\nCaptured: %s\n\nOWNERS\n",
		o.GetKind(), o.GetNamespace(), o.GetName(), uid, time.Now().UTC().Format(time.RFC3339))
	for _, r := range o.GetOwnerReferences() {
		fmt.Fprintf(&b, "%s %s (%s)\n", r.Kind, r.Name, r.APIVersion)
	}
	if len(o.GetOwnerReferences()) == 0 {
		b.WriteString("No owner references reported.\n")
	}
	b.WriteString("\nCONDITIONS\n")
	conditions, _, _ := unstructured.NestedSlice(o.Object, "status", "conditions")
	for _, c := range conditions {
		if m, ok := c.(map[string]any); ok {
			fmt.Fprintf(&b, "%v: %v", m["type"], m["status"])
			if reason, ok := m["reason"].(string); ok && reason != "" {
				fmt.Fprintf(&b, " | %s", reason)
			}
			b.WriteString("\n")
			if message, ok := m["message"].(string); ok && message != "" {
				fmt.Fprintf(&b, "  %s\n", message)
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
				fmt.Fprintf(&b, "%v: ready=%v restarts=%v\n", m["name"], m["ready"], m["restartCount"])
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
						}
					}
				}
			}
		}
	}
	return b.String()
}

func inspectionMarkup(a *App, text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		switch {
		case line == "TRUST SOURCE" || line == "VERIFIED TLS HANDSHAKE" || line == "TLS CONFIGURATION REFERENCES" || strings.HasPrefix(line, "WORKLOAD PODS (") ||
			line == "OWNERS" || line == "CONDITIONS" || line == "READ-ONLY SNAPSHOT" ||
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
