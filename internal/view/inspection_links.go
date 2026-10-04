// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
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
	"k8s.io/apimachinery/pkg/types"
)

const (
	inspectionPodKind         = "Pod"
	inspectionDeploymentKind  = "Deployment"
	inspectionDaemonSetKind   = "DaemonSet"
	inspectionStatefulSetKind = "StatefulSet"
	inspectionReplicaSetKind  = "ReplicaSet"
	inspectionJobKind         = "Job"
)

type inspectionReference struct {
	ref    certmanager.Reference
	notice string
	reason string
	uid    types.UID
}

func (d *inspectionDetails) openRelated() {
	if d.contextName != d.app.Config.ActiveContextName() {
		d.app.Flash().Warn("Context changed; reopen inspection")
		return
	}
	if d.related == nil {
		return
	}
	if d.snapshot.Text == "" {
		d.app.Flash().Info("Wait for the source snapshot before opening related resources")
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
	target := d.target
	go func() {
		defer cancel()
		refs, err := d.related(ctx, target)
		if ctx.Err() != nil {
			err = ctx.Err()
		}
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
			p := &relatedPicker{Picker: NewPicker()}
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
					if item.uid != "" {
						text += " | UID " + string(item.uid)
					} else {
						text += " | UID unknown (name reference)"
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
				target := SelectedResourceTarget{Context: contextName, GVR: gvr, Namespace: item.ref.Namespace, Name: item.ref.Name, UID: item.uid}
				if !namespaced {
					target.Namespace = ""
				}
				d.followRelated(p, target, command, client.FQN(ns, item.ref.Name))
			})
		})
	}()
}

// A relationship jump is another bounded read of the pinned context. Closing
// the picker cancels it, and a delayed reply cannot navigate a different page.
type relatedPicker struct {
	*Picker
	cancel     context.CancelFunc
	generation uint64
	namespace  string
	revision   uint64
}

func (p *relatedPicker) Stop() {
	p.generation++
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
}

//nolint:gocritic // A jump captures an immutable identity before its asynchronous read.
func (d *inspectionDetails) followRelated(p *relatedPicker, target SelectedResourceTarget, command, path string) {
	if d.connection == nil {
		d.app.Flash().Warn("Related target visibility unavailable; reopen the source inspection")
		return
	}
	if p.cancel != nil {
		p.cancel()
	}
	p.generation++
	generation := p.generation
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	p.cancel = cancel
	p.namespace = d.app.Config.ActiveNamespace()
	p.revision = d.app.Config.DestinationRevision()
	d.app.Flash().Info("Checking related resource identity...")
	go func() {
		observed, err := loadRelatedTarget(ctx, d.connection, target)
		d.app.QueueUpdateDraw(func() {
			defer cancel()
			d.applyRelatedJump(ctx, p, generation, observed, err, command, path)
		})
	}()
}

//nolint:gocritic // Navigation consumes the immutable identity captured by the bounded read.
func (d *inspectionDetails) applyRelatedJump(ctx context.Context, p *relatedPicker, generation uint64,
	observed SelectedResourceTarget, err error, command, path string,
) {
	if d.app.Content.Top() != p || p.generation != generation || d.app.Config.ActiveContextName() != observed.Context ||
		d.app.Config.ActiveNamespace() != p.namespace || d.app.Config.DestinationRevision() != p.revision {
		return
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		d.app.Flash().Err(err)
		return
	}
	sourceNamespace := d.app.Config.ActiveNamespace()
	ownership := d.ownedReturnDestinations(sourceNamespace)
	d.app.PrevCmd(nil)
	d.app.gotoResource(command, path, false, true)
	if v, ok := d.app.Content.Top().(ResourceViewer); ok && v.GetTable() != nil && v.GVR() == observed.GVR {
		v.GetTable().expectedTarget = &observed
		// Only the successful jump owns a return namespace. A subsequent user
		// namespace/context change invalidates it before Back can restore it.
		d.rememberRelatedDestination(sourceNamespace, ownership)
	} else if d.app.Content.Top() == d {
		// A command can change the namespace before destination Init fails.
		// Keep the still-visible source snapshot and destination chrome coherent.
		d.rememberRelatedDestination(sourceNamespace, ownership)
		d.restoreNavigationNamespace()
	}
	if observed.UID == "" {
		d.app.Flash().Warn("Related resource UID unknown; continuity cannot be verified")
	}
}

func (d *inspectionDetails) ownedReturnDestinations(sourceNamespace string) []inspectionReturnOwnership {
	var ownership []inspectionReturnOwnership
	revision := d.app.Config.DestinationRevision()
	for _, component := range d.app.Content.Peek() {
		ancestor, ok := component.(*inspectionDetails)
		if !ok || ancestor == d || ancestor.contextName != d.contextName {
			continue
		}
		destination := ancestor.returnDestination
		if destination != nil && destination.revision == revision && destination.destinationNamespace == sourceNamespace {
			ownership = append(ownership, inspectionReturnOwnership{inspector: ancestor, destination: destination, revision: revision})
		}
	}
	return ownership
}

func (d *inspectionDetails) rememberRelatedDestination(sourceNamespace string, ownership []inspectionReturnOwnership) {
	d.returnDestination = &inspectionReturnDestination{
		sourceNamespace:      sourceNamespace,
		destinationNamespace: d.app.Config.ActiveNamespace(),
		revision:             d.app.Config.DestinationRevision(),
		ancestors:            ownership,
	}
}

//nolint:gocritic // This read owns an immutable target value and returns its observed identity.
func loadRelatedTarget(ctx context.Context, conn client.Connection, target SelectedResourceTarget) (SelectedResourceTarget, error) {
	if err := target.Err(); err != nil {
		return target, err
	}
	dyn, err := conn.DynDial()
	if err != nil {
		return target, err
	}
	obj, err := dyn.Resource(target.GVR.GVR()).Namespace(target.Namespace).Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return target, err
	}
	if err := ctx.Err(); err != nil {
		return target, err
	}
	if err := verifySelectedIdentity(target, obj); err != nil {
		return target, err
	}
	target.UID = obj.GetUID()
	return target, nil
}

//nolint:gocritic // Resource identity is an immutable value captured before asynchronous reads.
func loadTargetInspectionReferences(ctx context.Context, conn client.Connection, target SelectedResourceTarget, name string) ([]inspectionReference, error) {
	if err := target.Err(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dyn, err := conn.DynDial()
	if err != nil {
		return nil, err
	}
	obj, err := dyn.Resource(target.GVR.GVR()).Namespace(target.Namespace).Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := verifySelectedIdentity(target, obj); err != nil {
		return nil, err
	}
	refs := objectReferences(obj)
	if target.UID == "" {
		refs = append(refs, inspectionReference{notice: "Source UID unknown; continuity with the captured source cannot be verified"})
	}
	if name == tlsCommand && inspectionKind(obj) == inspectionSecretKind {
		refs = append(refs, tlsConsumers(ctx, conn, obj)...)
	}
	if name == troubleshootCommand {
		pods, notice := workloadPods(ctx, conn, obj)
		for _, pod := range pods {
			refs = append(refs, inspectionReference{
				ref: certmanager.Reference{Kind: inspectionPodKind, Namespace: pod.GetNamespace(), Name: pod.GetName()},
				uid: pod.GetUID(), reason: "workload selector match; current API identity",
			})
		}
		if notice != "" {
			refs = append(refs, inspectionReference{notice: notice})
		}
	}
	refs = append(refs, networkRelationships(ctx, conn, obj)...)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return stableRelationships(refs), nil
}

func objectReferences(o *unstructured.Unstructured) []inspectionReference {
	var refs []inspectionReference
	for _, owner := range o.GetOwnerReferences() {
		gv, err := schema.ParseGroupVersion(owner.APIVersion)
		if err == nil {
			ref := relationshipRef(gv.Group, owner.Kind, o.GetNamespace(), owner.Name, "ownerReference")
			ref.uid = owner.UID
			refs = append(refs, ref)
		}
	}
	if inspectionKind(o) == inspectionCertificateKind {
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
	switch inspectionKind(o) {
	case inspectionDeploymentKind, inspectionDaemonSetKind, inspectionStatefulSetKind, inspectionReplicaSetKind, inspectionJobKind:
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

// APIVersion is always present on API reads. Empty versions are accepted only
// for older in-memory summary fixtures; explicit group collisions are rejected.
func inspectionKind(o *unstructured.Unstructured) string {
	expected := map[string]string{
		inspectionPodKind: "", inspectionSecretKind: "", inspectionCertificateKind: "cert-manager.io",
		inspectionDeploymentKind: "apps", inspectionDaemonSetKind: "apps", inspectionStatefulSetKind: "apps", inspectionReplicaSetKind: "apps", inspectionJobKind: "batch",
		"Ingress": "networking.k8s.io", "Gateway": "gateway.networking.k8s.io",
	}
	group, supported := expected[o.GetKind()]
	if !supported || (o.GetAPIVersion() != "" && o.GroupVersionKind().Group != group) {
		return ""
	}
	return o.GetKind()
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
