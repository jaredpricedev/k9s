// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package model

import (
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/model1"
)

// ColorerFor uses the same renderer resolution as table data, including
// dynamically discovered resource kinds which have no static registry entry.
func ColorerFor(gvr *client.GVR) model1.ColorerFunc {
	if renderer := resourceMeta(gvr).Renderer; renderer != nil {
		return renderer.ColorerFunc()
	}
	return model1.DefaultColorer
}
