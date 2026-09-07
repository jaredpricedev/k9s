// SPDX-License-Identifier: Apache-2.0
package view

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/certmanager"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/tview"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type inspectionReference struct {
	ref    certmanager.Reference
	notice string
	reason string
}

func (d *inspectionDetails) openRelated() {
	if d.contextName != d.app.Config.ActiveContextName() {
		d.app.Flash().Warn("Context changed; reopen inspection")
		return
	}
	if d.related == nil {
		return
	}
	if d.cancel != nil {
		d.cancel()
	}
	d.generation++
	generation := d.generation
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	d.cancel = cancel
	d.app.Flash().Info("Loading related resources...")
	go func() {
		defer cancel()
		refs, err := d.related(ctx)
		d.app.QueueUpdateDraw(func() {
			if d.app.Content.Top() != d || generation != d.generation || d.contextName != d.app.Config.ActiveContextName() {
				return
			}
			if err != nil {
				d.app.Flash().Err(err)
				return
			}
			if len(refs) == 0 {
				d.app.Flash().Info("No related resources reported")
				return
			}
			p := NewPicker()
			contextName := d.contextName
			if err := d.app.inject(p, false); err != nil {
				d.app.Flash().Err(err)
				return
			}
			p.SetTitle(" Related resources | Enter jump | Esc back ")
			for _, item := range refs {
				text := item.notice
				if text == "" {
					text = item.ref.Kind + " " + client.FQN(item.ref.Namespace, item.ref.Name)
					if item.reason != "" {
						text += " | " + item.reason
					}
				}
				p.AddItem(tview.Escape(text), "", 0, nil)
			}
			p.SetSelectedFunc(func(i int, _, _ string, _ rune) {
				if i < 0 || i >= len(refs) {
					return
				}
				item := refs[i]
				if item.notice != "" {
					d.app.Flash().Info(item.notice)
					return
				}
				if contextName != d.app.Config.ActiveContextName() {
					d.app.Flash().Warn("Context changed; reopen related resources")
					return
				}
				gvr, namespaced, err := resolveCertificateReference(dao.MetaAccess, item.ref)
				if err != nil {
					d.app.Flash().Err(err)
					return
				}
				ns := client.ClusterScope
				command := gvr.String()
				if namespaced {
					ns = item.ref.Namespace
					if ns == "" {
						d.app.Flash().Warn("Namespace is unavailable")
						return
					}
					command += " " + ns
				}
				d.app.PrevCmd(nil)
				d.app.gotoResource(command, client.FQN(ns, item.ref.Name), false, true)
			})
		})
	}()
}

func loadInspectionReferences(ctx context.Context, conn client.Connection, gvr *client.GVR, path, name string) ([]inspectionReference, error) {
	dyn, err := conn.DynDial()
	if err != nil {
		return nil, err
	}
	ns, n := client.Namespaced(path)
	obj, err := dyn.Resource(gvr.GVR()).Namespace(ns).Get(ctx, n, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	refs := objectReferences(obj)
	if name == tlsCommand && obj.GetKind() == inspectionSecretKind {
		refs = append(refs, tlsConsumers(ctx, conn, obj)...)
	}
	if name == troubleshootCommand {
		pods, notice := workloadPods(ctx, conn, obj)
		for _, pod := range pods {
			refs = append(refs, inspectionReference{ref: certmanager.Reference{Kind: "Pod", Namespace: pod.GetNamespace(), Name: pod.GetName()}})
		}
		if notice != "" {
			refs = append(refs, inspectionReference{notice: notice})
		}
	}
	refs = append(refs, networkRelationships(ctx, conn, obj)...)
	return stableRelationships(refs), nil
}

func objectReferences(o *unstructured.Unstructured) []inspectionReference {
	var refs []inspectionReference
	for _, owner := range o.GetOwnerReferences() {
		gv, err := schema.ParseGroupVersion(owner.APIVersion)
		if err == nil {
			refs = append(refs, relationshipRef(gv.Group, owner.Kind, o.GetNamespace(), owner.Name, "ownerReference"))
		}
	}
	if o.GetKind() == inspectionCertificateKind {
		for _, ref := range certmanager.References(o) {
			if ref.Kind != "" {
				refs = append(refs, inspectionReference{ref: ref})
			}
		}
	}
	for _, ref := range tlsSecretReferences(o) {
		refs = append(refs, inspectionReference{ref: ref})
	}
	refs = append(refs, networkReferences(o)...)
	seen := map[inspectionReference]bool{}
	unique := refs[:0]
	for _, ref := range refs {
		if !seen[ref] {
			seen[ref] = true
			unique = append(unique, ref)
		}
	}
	return unique
}

func workloadPods(ctx context.Context, conn client.Connection, o *unstructured.Unstructured) (pods []*unstructured.Unstructured, notice string) {
	switch o.GetKind() {
	case "Deployment", "DaemonSet", "StatefulSet", "ReplicaSet", "Job":
	default:
		return nil, ""
	}
	raw, ok, _ := unstructured.NestedMap(o.Object, "spec", "selector")
	if !ok {
		return nil, "Pod visibility unavailable: selector missing"
	}
	var selector metav1.LabelSelector
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &selector); err != nil {
		return nil, "Pod visibility unavailable: invalid selector"
	}
	sel, err := metav1.LabelSelectorAsSelector(&selector)
	if err != nil || sel.Empty() {
		return nil, "Pod visibility unavailable: empty or invalid selector"
	}
	dyn, err := conn.DynDial()
	if err != nil {
		return nil, err.Error()
	}
	list, err := dyn.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(o.GetNamespace()).List(ctx, metav1.ListOptions{
		LabelSelector: sel.String(), Limit: 100,
	})
	if err != nil {
		return nil, "Pod visibility unavailable: " + err.Error()
	}
	pods = make([]*unstructured.Unstructured, 0, len(list.Items))
	for i := range list.Items {
		pods = append(pods, &list.Items[i])
	}
	notice = ""
	if len(pods) == 0 {
		notice = "No current pods match the workload selector"
	}
	if list.GetContinue() != "" {
		notice = "Pod results truncated at 100; narrow the workload scope"
	}
	return pods, notice
}
func workloadDiagnostics(ctx context.Context, conn client.Connection, o *unstructured.Unstructured) string {
	pods, notice := workloadPods(ctx, conn, o)
	if len(pods) == 0 && notice == "" {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nWORKLOAD PODS (selector matches; %d observed in this snapshot)\n", len(pods))
	for _, pod := range pods {
		fmt.Fprintf(&b, "\nPod %s | node %s\n", pod.GetName(), nestedText(pod.Object, "spec", "nodeName"))
		phase := nestedText(pod.Object, "status", "phase")
		fmt.Fprintf(&b, "Phase: %s\n", phase)
		for _, field := range []string{"initContainerStatuses", "containerStatuses"} {
			rows, _, _ := unstructured.NestedSlice(pod.Object, "status", field)
			for _, row := range rows {
				if m, ok := row.(map[string]any); ok {
					fmt.Fprintf(&b, "  %v ready=%v restarts=%v\n", m["name"], m["ready"], m["restartCount"])
					for _, s := range []string{"state", "lastState"} {
						for _, p := range []string{"waiting", "terminated"} {
							v, found, _ := unstructured.NestedMap(m, s, p)
							if found {
								fmt.Fprintf(&b, "    %s %s reason=%v exitCode=%v\n", s, p, v["reason"], v["exitCode"])
							}
						}
					}
				}
			}
		}
	}
	if notice != "" {
		b.WriteString(notice + "\n")
	}
	b.WriteString("Use g to jump to a pod for its events and logs.\n")
	return b.String()
}
func nestedText(o map[string]any, fields ...string) string {
	s, _, _ := unstructured.NestedString(o, fields...)
	return s
}
