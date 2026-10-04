// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dependency"
	"github.com/derailed/k9s/internal/networkpath"
	"github.com/derailed/tcell/v2"
	"k8s.io/apimachinery/pkg/types"
)

const dependencyReviewCommandToken = "dependency-review"

func (c *Command) dependencyReviewCommand() {
	if view, ok := c.app.Content.Top().(*networkReviewView); ok {
		view.selectTab(7)
		return
	}
	owner, ok := c.app.Content.Top().(actionOwner)
	if !ok {
		c.app.Flash().Warn("Select a native Service to review explicit dependency evidence")
		return
	}
	target := actionTarget(owner, c.app.Config.ActiveContextName())
	if target.GVR == nil || target.GVR.GVR() != client.SvcGVR.GVR() || target.Err() != nil {
		c.app.Flash().Warn("Select a native Service with captured UID; broad maps remain demand-gated")
		return
	}
	c.app.openNetworkReviewTab(target, nil, 7)
}

func (v *networkReviewView) renderEdges(width, height int) string {
	list := make([]networkpath.Item, len(v.edgeItems))
	copy(list, v.edgeItems)
	for i := range list {
		list[i].Group = networkpath.GroupDNS
	}
	return (&networkpath.Snapshot{Items: list}).Render(1, v.selectedIndex(), width, height-8)
}

func (v *networkReviewView) edgeTarget() SelectedResourceTarget {
	reason := "Select an edge in the captured destination first"
	if v.activeTab == 7 && v.destinationCurrent() {
		if item := v.selectedItem(); item != nil {
			for _, id := range item.Related {
				if id.UID != "" && id != item.Source.Identity {
					return SelectedResourceTarget{Context: id.Context, GVR: client.NewGVR(id.GVR), Namespace: id.Namespace, Name: id.Name, UID: types.UID(id.UID)}
				}
			}
		}
		reason = "No captured native target UID; reported peers remain uncertain"
	}
	return SelectedResourceTarget{Context: v.target.Context, UnavailableReason: reason}
}
func (v *networkReviewView) inspectEdgeTarget(e *tcell.EventKey) *tcell.EventKey {
	if v.cmdBuff.IsActive() {
		return e
	}
	target := v.edgeTarget()
	if err := target.Err(); err != nil {
		v.app.Flash().Warn(err.Error())
		return nil
	}
	v.app.openTargetInspection(target, troubleshootCommand)
	return nil
}

func (v *networkReviewView) rebuildDependencies() {
	v.dependencies = dependency.Compose(v.snapshot)
	v.edgeItems = v.dependencies.Items()
}
