// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package render

import (
	"fmt"
	"strings"

	"github.com/derailed/k9s/internal/client"
	fluxmodel "github.com/derailed/k9s/internal/flux"
	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/tcell/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const fluxReadyStatus = "Ready"

var defaultFluxHeader = model1.Header{
	model1.HeaderColumn{Name: "NAMESPACE"},
	model1.HeaderColumn{Name: "NAME"},
	model1.HeaderColumn{Name: "KIND"},
	model1.HeaderColumn{Name: "STATUS"},
	model1.HeaderColumn{Name: "SUSPEND"},
	model1.HeaderColumn{Name: "REVISION"},
	model1.HeaderColumn{Name: "SOURCE"},
	model1.HeaderColumn{Name: "MESSAGE", Attrs: model1.Attrs{Wide: true}},
	model1.HeaderColumn{Name: "AGE", Attrs: model1.Attrs{Time: true}},
	model1.HeaderColumn{Name: "LABELS", Attrs: model1.Attrs{Wide: true}},
}

// Flux renders both a native Flux kind and the unified multi-kind Flux view.
type Flux struct {
	Base
	Unified bool
}

// ColorerFunc colors Flux severity using the active theme, preserving deletion
// and invalid-row feedback from the normal resource renderer.
func (Flux) ColorerFunc() model1.ColorerFunc {
	return func(ns string, h model1.Header, re *model1.RowEvent) tcell.Color {
		color := model1.DefaultColorer(ns, h, re)
		if !model1.IsValid(ns, h, re.Row) || re.Kind == model1.EventDelete {
			return color
		}
		index, ok := h.IndexOf("STATUS", true)
		if !ok || index >= len(re.Row.Fields) {
			return color
		}
		switch strings.TrimSpace(re.Row.Fields[index]) {
		case "Failed", "Restricted":
			return model1.ErrColor
		case PhaseUnknown, "Reconciling", "Pending":
			return model1.PendingColor
		case "Suspended":
			return model1.HighlightColor
		case fluxReadyStatus:
			return model1.CompletedColor
		default:
			return color
		}
	}
}

// Header keeps verbose details in wide mode, retaining kind in the unified view.
func (f Flux) Header(string) model1.Header {
	return f.doHeader(f.defaultHeader())
}

func (f Flux) defaultHeader() model1.Header {
	header := defaultFluxHeader.Clone()
	header[2].Wide = !f.Unified
	return header
}

// Render renders a Flux unstructured resource.
func (f Flux) Render(value any, _ string, row *model1.Row) error {
	o, ok := value.(*unstructured.Unstructured)
	if !ok || o == nil {
		return fmt.Errorf("expected Unstructured, but got %T", value)
	}
	f.defaultRow(o, row)
	if f.specs.isEmpty() {
		return nil
	}
	cols, err := f.specs.realize(o, f.defaultHeader(), row)
	cols.hydrateRow(row)
	return err
}

func (f Flux) defaultRow(o *unstructured.Unstructured, row *model1.Row) {
	meta := metav1.ObjectMeta{Namespace: o.GetNamespace(), Name: o.GetName()}
	id := client.MetaFQN(&meta)
	if f.Unified {
		id = fluxmodel.GVRFor(o).String() + "|" + id
	}
	state, message := fluxmodel.Status(o)
	revision := fluxmodel.Revision(o)
	if revision == "" {
		revision = MissingValue
	}
	source := MissingValue
	if ref, ok := fluxmodel.Source(o); ok {
		source = fmt.Sprintf("%s:%s", ref.Kind, client.FQN(ref.Namespace, ref.Name))
	}
	row.ID = id
	row.Fields = model1.Fields{
		o.GetNamespace(),
		o.GetName(),
		o.GetKind(),
		state,
		boolToStr(fluxmodel.Suspended(o)),
		revision,
		source,
		message,
		ToAge(o.GetCreationTimestamp()),
		mapToStr(o.GetLabels()),
	}
}
