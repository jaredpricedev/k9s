// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package view

import (
	"fmt"
	"sort"
	"strings"

	"github.com/derailed/k9s/internal/certmanager"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/ui/dialog"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
)

// CertManager adds diagnostic navigation to the native cert-manager tables.
type CertManager struct{ ResourceViewer }

func NewCertManager(gvr *client.GVR) ResourceViewer {
	c := &CertManager{ResourceViewer: NewBrowser(gvr)}
	c.GetTable().SetLiteralFields(true)
	c.AddBindKeysFn(c.bindKeys)
	if gvr.R() == "certificates" {
		c.GetTable().SetSortCol("EXPIRES", true)
	}
	return c
}

func (c *CertManager) bindKeys(actions *ui.KeyActions) {
	actions.Add(ui.KeyG, ui.NewKeyAction("Issuance/References", c.relatedCmd, true))
	actions.Add(ui.KeyI, ui.NewKeyAction("Certificate Status", c.statusCmd, true))
}

func (c *CertManager) statusCmd(evt *tcell.EventKey) *tcell.EventKey {
	path := c.GetTable().GetSelectedItem()
	if path == "" {
		return evt
	}
	row := c.GetTable().GetSelectedRow(path)
	if row == nil {
		return evt
	}
	header := c.GetTable().GetModel().Peek().Header()
	details := NewDetails(c.App(), "Certificate Status", path, contentTXT, false).Update(certificateStatusDetails(header, row))
	if err := c.App().inject(details, false); err != nil {
		c.App().Flash().Err(err)
	}
	return nil
}

func certificateStatusDetails(header model1.Header, row *model1.Row) string {
	if row == nil {
		return ""
	}
	var out strings.Builder
	for _, name := range []string{"NAMESPACE", "NAME", "STATUS", "EXPIRES", "RENEWAL", "ISSUER", "SECRET", "DNS NAMES", "MESSAGE", "AGE"} {
		if idx, ok := header.IndexOf(name, true); ok && idx < len(row.Fields) {
			fmt.Fprintf(&out, "%s: %s\n", name, row.Fields[idx])
		}
	}
	return tview.Escape(out.String())
}

type certificateRelatedItem struct {
	ref    certmanager.Reference
	notice string
}

func (c *CertManager) relatedCmd(evt *tcell.EventKey) *tcell.EventKey {
	path := c.GetTable().GetSelectedItem()
	if path == "" {
		return evt
	}
	obj, err := c.App().factory.Get(c.GVR(), path, true, labels.Everything())
	if err != nil {
		c.App().Flash().Err(err)
		return nil
	}
	o, ok := obj.(*unstructured.Unstructured)
	if !ok || o == nil {
		c.App().Flash().Warn("Certificate resource is unavailable")
		return nil
	}
	items := certificateRelatedItems(c.App().factory, dao.MetaAccess, o)
	if len(items) == 0 {
		c.App().Flash().Info("No issuer, owner or issuance references on this resource")
		return nil
	}
	options := make([]string, len(items))
	for i, item := range items {
		label := item.notice
		if label == "" {
			label = fmt.Sprintf("%s: %s", item.ref.Kind, client.FQN(item.ref.Namespace, item.ref.Name))
		}
		options[i] = tview.Escape(label)
	}
	contextName := c.App().Config.ActiveContextName()
	d := c.App().Styles.Dialog()
	dialog.ShowSelection(&d, c.App().Content.Pages, "Certificate relationships", options, func(index int) {
		if index < 0 || index >= len(items) {
			return
		}
		item := items[index]
		if item.notice != "" {
			c.App().Flash().Info(item.notice)
			return
		}
		if c.App().Config.ActiveContextName() != contextName {
			c.App().Flash().Warn("Context changed; open certificate relationships again")
			return
		}
		gvr, namespaced, err := resolveCertificateReference(dao.MetaAccess, item.ref)
		if err != nil {
			c.App().Flash().Err(err)
			return
		}
		ns := client.ClusterScope
		command := gvr.String()
		if namespaced {
			ns = item.ref.Namespace
			if ns == "" {
				c.App().Flash().Warn("Referenced namespaced resource has no namespace")
				return
			}
			command += " " + ns
		}
		c.App().gotoResource(command, client.FQN(ns, item.ref.Name), false, true)
	})
	return nil
}

func certificateRelatedItems(f dao.Factory, metas *dao.Meta, o *unstructured.Unstructured) []certificateRelatedItem {
	items := make([]certificateRelatedItem, 0)
	for _, ref := range certmanager.References(o) {
		items = append(items, certificateRelatedItem{ref: ref})
	}
	child := certificateChildType(o.GetKind())
	if child.Kind == "" || o.GetNamespace() == "" || o.GetUID() == "" {
		return items
	}
	gvr, _, err := resolveCertificateReference(metas, child)
	if err != nil {
		return append(items, certificateRelatedItem{notice: err.Error()})
	}
	objects, err := f.List(gvr, o.GetNamespace(), false, labels.Everything())
	if err != nil {
		return append(items, certificateRelatedItem{notice: fmt.Sprintf("Cannot list %s: %v", child.Kind, err)})
	}
	children := certificateChildren(o, objects)
	for _, ref := range children {
		items = append(items, certificateRelatedItem{ref: ref})
	}
	inf, err := f.CanForResource(o.GetNamespace(), gvr, client.ListAccess)
	if err != nil {
		return append(items, certificateRelatedItem{notice: fmt.Sprintf("Cannot check %s cache: %v", child.Kind, err)})
	}
	if inf == nil || !inf.Informer().HasSynced() {
		items = append(items, certificateRelatedItem{notice: fmt.Sprintf("Loading %s cache; close and press g again to refresh", child.Kind)})
	} else if len(children) == 0 {
		items = append(items, certificateRelatedItem{notice: "No owned " + child.Kind + " resources found"})
	}
	return items
}

func certificateChildType(kind string) certmanager.Reference {
	switch kind {
	case "Certificate":
		return certmanager.Reference{Group: "cert-manager.io", Kind: "CertificateRequest"}
	case "CertificateRequest":
		return certmanager.Reference{Group: "acme.cert-manager.io", Kind: "Order"}
	case "Order":
		return certmanager.Reference{Group: "acme.cert-manager.io", Kind: "Challenge"}
	default:
		return certmanager.Reference{}
	}
}

func certificateChildren(parent *unstructured.Unstructured, objects []runtime.Object) []certmanager.Reference {
	var refs []certmanager.Reference
	if parent == nil || parent.GetUID() == "" {
		return refs
	}
	child := certificateChildType(parent.GetKind())
	for _, obj := range objects {
		o, ok := obj.(*unstructured.Unstructured)
		if !ok || o == nil || o.GetNamespace() != parent.GetNamespace() || o.GetKind() != child.Kind || o.GroupVersionKind().Group != child.Group {
			continue
		}
		for _, owner := range o.GetOwnerReferences() {
			gv, err := schema.ParseGroupVersion(owner.APIVersion)
			if err == nil && gv.Group == parent.GroupVersionKind().Group && owner.Kind == parent.GetKind() && owner.UID == parent.GetUID() && owner.Name == parent.GetName() {
				refs = append(refs, certmanager.Reference{Group: child.Group, Kind: child.Kind, Namespace: o.GetNamespace(), Name: o.GetName()})
				break
			}
		}
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
	return refs
}

func resolveCertificateReference(metas *dao.Meta, ref certmanager.Reference) (*client.GVR, bool, error) {
	best := client.NoGVR
	namespaced := false
	for _, gvr := range metas.AllGVRs() {
		if gvr.G() != ref.Group {
			continue
		}
		meta, err := metas.MetaFor(gvr)
		if err != nil || meta.Kind != ref.Kind {
			continue
		}
		if best == client.NoGVR || version.CompareKubeAwareVersionStrings(gvr.V(), best.V()) > 0 {
			best, namespaced = gvr, meta.Namespaced
		}
	}
	if best == client.NoGVR {
		return best, false, fmt.Errorf("%s in group %s is not available in this context", ref.Kind, ref.Group)
	}
	return best, namespaced, nil
}
