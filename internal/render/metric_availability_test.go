// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package render_test

import (
	"testing"

	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/k9s/internal/render"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	mv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

func TestPodUsageUnknownIsDistinctFromMeasuredZeroAndPartialCoverage(t *testing.T) {
	for _, test := range []struct {
		name                string
		metrics             *mv1beta1.PodMetrics
		secondContainer     bool
		wantCPU, wantMemory string
	}{
		{name: "missing API sample", wantCPU: render.NAValue, wantMemory: render.NAValue},
		{name: "empty API sample", metrics: &mv1beta1.PodMetrics{}, wantCPU: render.NAValue, wantMemory: render.NAValue},
		{name: "measured zero", metrics: makePodMX("nginx", "0", "0"), wantCPU: "0", wantMemory: "0"},
		{name: "missing memory dimension", metrics: &mv1beta1.PodMetrics{Containers: []mv1beta1.ContainerMetrics{{Name: "nginx", Usage: v1.ResourceList{v1.ResourceCPU: toQty("100m")}}}}, wantCPU: "100", wantMemory: render.NAValue},
		{name: "missing CPU dimension", metrics: &mv1beta1.PodMetrics{Containers: []mv1beta1.ContainerMetrics{{Name: "nginx", Usage: v1.ResourceList{v1.ResourceMemory: toQty("50Mi")}}}}, wantCPU: render.NAValue, wantMemory: "50"},
		{name: "partial container coverage", metrics: makePodMX("nginx", "100m", "50Mi"), secondContainer: true, wantCPU: render.NAValue, wantMemory: render.NAValue},
	} {
		t.Run(test.name, func(t *testing.T) {
			object := load(t, "po")
			if test.secondContainer {
				spec := object.Object["spec"].(map[string]any)
				spec["containers"] = append(spec["containers"].([]any), map[string]any{"name": "sidecar", "image": "sidecar"})
			}
			pod := render.PodWithMetrics{Raw: object, MX: test.metrics}
			row := model1.NewRow(14)
			require.NoError(t, render.NewPod().Render(&pod, "", &row))
			require.Equal(t, test.wantCPU, row.Fields[8])
			require.Equal(t, test.wantMemory, row.Fields[12])
			if test.wantCPU == render.NAValue {
				require.Equal(t, render.NAValue, row.Fields[10])
				require.Equal(t, render.NAValue, row.Fields[11])
			}
			if test.wantMemory == render.NAValue {
				require.Equal(t, render.NAValue, row.Fields[14])
				require.Equal(t, render.NAValue, row.Fields[15])
			}
			require.Equal(t, "100:0", row.Fields[9], "requests and limits remain known from the resource spec")
			require.Equal(t, "70:170", row.Fields[13])
		})
	}
}

func TestContainerAndNodeUsageUnknownIsDistinctFromMeasuredZero(t *testing.T) {
	for _, missing := range []bool{true, false} {
		name, expected := "measured zero", "0"
		var containerMetrics *mv1beta1.ContainerMetrics
		var nodeMetrics *mv1beta1.NodeMetrics
		if missing {
			name, expected = "missing sample", render.NAValue
		} else {
			containerMetrics = &mv1beta1.ContainerMetrics{Usage: makeRes("0", "0")}
			nodeMetrics = makeNodeMX("n1", "0", "0")
		}
		t.Run(name, func(t *testing.T) {
			var container render.Container
			row := model1.NewRow(14)
			require.NoError(t, container.Render(render.ContainerRes{Container: makeContainer(), Status: makeContainerStatus(), MX: containerMetrics}, "", &row))
			for _, index := range []int{8, 10, 11, 12, 14, 15} {
				require.Equal(t, expected, row.Fields[index])
			}
			var node render.Node
			require.NoError(t, node.Render(&render.NodeWithMetrics{Raw: load(t, "no"), MX: nodeMetrics}, "", &row))
			for _, index := range []int{11, 13, 14, 16} {
				require.Equal(t, expected, row.Fields[index])
			}
		})
	}
}
