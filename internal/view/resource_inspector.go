// SPDX-License-Identifier: Apache-2.0
package view

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/inspect"
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
	cancel context.CancelFunc
}

func (d *inspectionDetails) Stop() {
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
	path := v.GetTable().GetSelectedItem()
	if name == actionsCommand {
		p := &actionPalette{Picker: NewPicker(), owner: v, app: c.app, path: path}
		if err := c.app.inject(p, false); err != nil {
			c.app.Flash().Err(err)
		}
		return
	}
	c.app.openInspection(v, name, path)
}
func (a *App) openInspection(v ResourceViewer, name, path string) {
	if path == "" {
		a.Flash().Err(fmt.Errorf("select a resource first"))
		return
	}
	if name == tlsCommand && v.GVR().R() != "secrets" {
		a.Flash().Err(fmt.Errorf("select a Secret containing tls.crt; use its UsedBy action for references"))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	d := &inspectionDetails{Details: NewDetails(a, name, path, contentTXT, true).Update("Loading read-only snapshot..."), cancel: cancel}
	if err := a.inject(d, false); err != nil {
		cancel()
		a.Flash().Err(err)
		return
	}
	gvr := v.GVR()
	connection := a.Conn()
	go func() {
		defer cancel()
		text, err := loadInspection(ctx, connection, gvr, path, name)
		if err != nil {
			text = "Inspection unavailable: " + err.Error()
		}
		a.QueueUpdateDraw(func() {
			if a.Content.Top() == d {
				d.Update(inspectionMarkup(a, text))
			}
		})
	}()
}

func loadInspection(ctx context.Context, conn client.Connection, gvr *client.GVR, path, name string) (string, error) {
	ns, resource := client.Namespaced(path)
	dyn, err := conn.DynDial()
	if err != nil {
		return "", err
	}
	obj, err := dyn.Resource(gvr.GVR()).Namespace(ns).Get(ctx, resource, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if name == tlsCommand {
		encoded, _, _ := unstructured.NestedString(obj.Object, "data", "tls.crt")
		data, decodeErr := base64.StdEncoding.DecodeString(encoded)
		if decodeErr != nil {
			return "", fmt.Errorf("tls.crt is not valid base64")
		}
		return inspect.Certificates(data, time.Now())
	}
	text := resourceSummary(obj)
	if obj.GetUID() == "" {
		return text + "\nEvents unavailable: object UID missing", nil
	}
	k, err := conn.Dial()
	if err != nil {
		return text + "\nEvents unavailable: " + err.Error(), nil
	}
	events, err := k.CoreV1().Events(ns).List(ctx, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("involvedObject.uid", string(obj.GetUID())).String(), Limit: 100,
	})
	if err != nil {
		return text + "\nEvents unavailable: " + err.Error(), nil
	}
	text += "\nEVENTS (API snapshot; not a complete history)\n"
	for i := range events.Items {
		e := &events.Items[i]
		text += fmt.Sprintf("%s  %s  %s  count=%d\n  %s\n", e.LastTimestamp.Time.UTC().Format(time.RFC3339), e.Type, e.Reason, e.Count, e.Message)
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
	fmt.Fprintf(&b, "READ-ONLY SNAPSHOT\n%s %s/%s\nUID: %s\nCaptured: %s\n\nOWNERS\n",
		o.GetKind(), o.GetNamespace(), o.GetName(), o.GetUID(), time.Now().UTC().Format(time.RFC3339))
	for _, r := range o.GetOwnerReferences() {
		fmt.Fprintf(&b, "%s %s (%s)\n", r.Kind, r.Name, r.APIVersion)
	}
	b.WriteString("\nCONDITIONS\n")
	conditions, _, _ := unstructured.NestedSlice(o.Object, "status", "conditions")
	for _, c := range conditions {
		if m, ok := c.(map[string]any); ok {
			fmt.Fprintf(&b, "%v: %v | %v\n  %v\n", m["type"], m["status"], m["reason"], m["message"])
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
							fmt.Fprintf(&b, "  %s %s: reason=%v exitCode=%v\n", state, phase, s["reason"], s["exitCode"])
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
		case line == "OWNERS" || line == "CONDITIONS" || line == "READ-ONLY SNAPSHOT" ||
			strings.HasPrefix(line, "CONTAINERS (") || strings.HasPrefix(line, "EVENTS (") || strings.HasPrefix(line, "CERTIFICATE "):
			lines[i] = detailStyled(a.Styles.Views().Yaml.KeyColor.String(), "b", line)
		case line == "EXPIRED" || line == "NOT YET VALID":
			lines[i] = detailStyled(a.Styles.K9s.Frame.Status.ErrorColor.String(), "b", line)
		default:
			lines[i] = wbText(line)
		}
	}
	return strings.Join(lines, "\n")
}
