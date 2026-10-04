// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/xray"
)

type xraySessionListener struct {
	view       *Xray
	model      *model.Tree
	generation uint64
}

func (l *xraySessionListener) TreeChanged(node *xray.TreeNode) {
	l.view.treeChanged(node, l.generation, l.model)
}

func (l *xraySessionListener) TreeLoadFailed(err error) {
	l.view.treeLoadFailed(err, l.generation, l.model)
}

func (x *Xray) sessionCurrent(generation uint64, source *model.Tree) bool {
	return x.generation.Load() == generation && x.model == source && x.app.IsRunning() && x.app.Content.Top() == x
}

func (x *Xray) treeChanged(node *xray.TreeNode, generation uint64, source *model.Tree) {
	go x.app.QueueUpdateDraw(func() {
		if !x.sessionCurrent(generation, source) {
			return
		}
		x.Count = node.Count(x.gvr)
		x.applyTree(x.filter(node))
		x.SetTitle(x.styleTitle())
	})
}

func (x *Xray) treeLoadFailed(err error, generation uint64, source *model.Tree) {
	go x.app.QueueUpdateDraw(func() {
		if x.sessionCurrent(generation, source) {
			x.app.Flash().Err(err)
		}
	})
}

func (x *Xray) rebindSession() {
	previous := x.model
	// Tree node references contain paths, not UIDs. Keep navigation/expansion,
	// but do not carry an unverified object selection into the fresh session.
	x.ClearSelection()
	x.model = model.NewTree(x.gvr)
	x.model.SetNamespace(previous.GetNamespace())
	x.model.SetRefreshRate(x.app.Config.K9s.RefreshDuration())
}
