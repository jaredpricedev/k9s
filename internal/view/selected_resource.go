// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"fmt"
	"strings"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// SelectedResourceTarget captures an API identity once, before an action or
// asynchronous read starts. An empty UID means the selected identity is unknown,
// never that any future object with the same name is the selected object.
type SelectedResourceTarget struct {
	Context           string
	GVR               *client.GVR
	Namespace, Name   string
	UID               types.UID
	UnavailableReason string
}

// SelectedResource is optional: a view may explicitly decline object selection
// while still supplying navigation actions through its ordinary Viewer API.
type SelectedResource interface {
	SelectedResource() SelectedResourceTarget
}

//nolint:gocritic // Value receiver supports immutable targets returned directly by selection resolvers.
func (t SelectedResourceTarget) Path() string { return client.FQN(t.Namespace, t.Name) }

//nolint:gocritic // Value receiver supports checking temporary immutable selection results.
func (t SelectedResourceTarget) Err() error {
	if t.UnavailableReason != "" {
		return fmt.Errorf("%s", t.UnavailableReason)
	}
	if t.GVR == nil || t.GVR.V() == "" || t.GVR.R() == "" || t.Name == "" {
		return fmt.Errorf("select an API resource first")
	}
	return nil
}

func resolveSelectedResource(v ResourceViewer, contextName string) SelectedResourceTarget {
	if resolver, ok := v.(SelectedResource); ok {
		target := resolver.SelectedResource()
		// The caller captures the current context even when a view has no App.
		target.Context = contextName
		return target
	}
	if v == nil || v.GetTable() == nil {
		return SelectedResourceTarget{Context: contextName, UnavailableReason: "This view has no selected API object; open a resource list first"}
	}
	return selectedResourceForPath(v, contextName, v.GetTable().GetSelectedItem())
}

// selectedResourceForPath also supports marked rows without changing the cursor.
// CachedGet is deliberately cache-only; selection never starts a network request.
func selectedResourceForPath(v ResourceViewer, contextName, path string) SelectedResourceTarget {
	if v == nil || v.GetTable() == nil {
		return SelectedResourceTarget{Context: contextName, UnavailableReason: "This view has no selected API object; open a resource list first"}
	}
	return tableResourceForPath(v.GetTable(), contextName, path)
}

func tableResourceForPath(t *Table, contextName, path string) SelectedResourceTarget {
	target := resourceTargetForPath(t.GVR(), contextName, path)
	if target.Err() != nil {
		return target
	}
	if a := t.App(); a != nil && a.factory != nil {
		if o, err := a.factory.CachedGet(target.GVR, t.GetNamespace(), target.Path()); err == nil {
			if m, err := meta.Accessor(o); err == nil {
				target.UID = m.GetUID()
			}
		}
	}
	return target
}

func resourceTargetForPath(gvr *client.GVR, contextName, path string) SelectedResourceTarget {
	target := SelectedResourceTarget{Context: contextName, GVR: gvr}
	if path == "" {
		target.UnavailableReason = "Select a resource first; this list has no selected object"
		return target
	}
	if gvr == client.FluxGVR {
		var ok bool
		target.GVR, path, ok = parseFluxPath(path)
		if !ok {
			target.UnavailableReason = "This Flux row is unavailable or restricted; see Status Details for its message"
			return target
		}
	}
	if target.GVR == nil || target.GVR.V() == "" || target.GVR.R() == "" || target.GVR.SubResource() != "" {
		target.UnavailableReason = "This view does not select an API object; open its native resource list first"
		return target
	}
	if m, err := dao.MetaAccess.MetaFor(target.GVR); err == nil && !dao.IsK8sMeta(m) {
		target.UnavailableReason = "This synthetic view does not select an API object; open its native resource list first"
		return target
	}
	if strings.ContainsAny(path, "<>|:") || strings.Count(path, "/") > 1 {
		target.UnavailableReason = "This row has no available API identity; select a resource row"
		return target
	}
	target.Namespace, target.Name = client.Namespaced(path)
	if client.IsClusterScoped(target.Namespace) {
		target.Namespace = ""
	}
	if target.Name == "" {
		target.UnavailableReason = "Select a resource first"
	}
	return target
}

func (b *Browser) SelectedResource() SelectedResourceTarget {
	contextName := ""
	if b.app != nil && b.app.Config != nil {
		contextName = b.app.Config.ActiveContextName()
	}
	return selectedResourceForPath(b, contextName, b.GetSelectedItem())
}

func (f *Flux) SelectedResource() SelectedResourceTarget {
	contextName := ""
	if a := f.App(); a != nil && a.Config != nil {
		contextName = a.Config.ActiveContextName()
	}
	return selectedResourceForPath(f, contextName, f.GetTable().GetSelectedItem())
}

func (*Pulse) SelectedResource() SelectedResourceTarget {
	return SelectedResourceTarget{UnavailableReason: "Pulse charts have no selected API object; open a resource list to inspect it"}
}

func (*Xray) SelectedResource() SelectedResourceTarget {
	return SelectedResourceTarget{UnavailableReason: "Xray is a navigation tree; use Goto to open a resource list before inspecting an object"}
}

//nolint:gocritic // Verification receives the captured immutable identity value.
func verifySelectedIdentity(target SelectedResourceTarget, obj metav1.Object) error {
	if target.UID == "" {
		return nil
	}
	if obj.GetUID() == "" {
		return fmt.Errorf("identity unavailable for %s: selected UID %s, current UID unknown; reopen the resource", target.Path(), target.UID)
	}
	if obj.GetUID() != target.UID {
		return fmt.Errorf("identity changed for %s: selected UID %s, current UID %s; this resource was replaced, reopen it", target.Path(), target.UID, obj.GetUID())
	}
	return nil
}
