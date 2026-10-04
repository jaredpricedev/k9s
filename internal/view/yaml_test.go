// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package view

import (
	"testing"

	"github.com/derailed/k9s/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestYaml(t *testing.T) {
	uu := []struct {
		s, e string
	}{
		{
			`api: fred
		   version: v1`,
			`[#b6a3df::b]api[#e1e7e3::-]: [#e1e7e3::]fred
		   [#b6a3df::b]version[#e1e7e3::-]: [#e1e7e3::]v1`,
		},
		{
			`api: <<<"search_0">>>fred<<<"">>>
		   version: v1`,
			`[#b6a3df::b]api[#e1e7e3::-]: [#e1e7e3::]["search_0"]fred[""]
		   [#b6a3df::b]version[#e1e7e3::-]: [#e1e7e3::]v1`,
		},
		{
			`api:
			version: v1`,
			`[#b6a3df::b]api[#e1e7e3::-]:
			[#b6a3df::b]version[#e1e7e3::-]: [#e1e7e3::]v1`,
		},
		{
			"      fred:blee",
			"[#e1e7e3::]      fred:blee",
		},
		{
			"fred blee: blee",
			"[#b6a3df::b]fred blee[#e1e7e3::-]: [#e1e7e3::]blee",
		},
		{
			"Node-Selectors:  <none>",
			"[#b6a3df::b]Node-Selectors[#e1e7e3::-]: [#e1e7e3::] <none>",
		},
		{
			"fred.blee:  <none>",
			"[#b6a3df::b]fred.blee[#e1e7e3::-]: [#e1e7e3::] <none>",
		},
		{
			"certmanager.k8s.io/cluster-issuer: nameOfClusterIssuer",
			"[#b6a3df::b]certmanager.k8s.io/cluster-issuer[#e1e7e3::-]: [#e1e7e3::]nameOfClusterIssuer",
		},
		{
			"Message: Pod The node was low on resource: [DiskPressure].",
			"[#b6a3df::b]Message[#e1e7e3::-]: [#e1e7e3::]Pod The node was low on resource: [DiskPressure[].",
		},
		{
			`data: "<<<"`,
			`[#b6a3df::b]data[#e1e7e3::-]: [#e1e7e3::]"<<<"`,
		},
	}

	s := config.NewStyles()
	for _, u := range uu {
		assert.Equal(t, u.e, colorizeYAML(s.Views().Yaml, u.s))
	}
}

func TestYAMLSkinColors(t *testing.T) {
	styles := config.NewStyles()
	for _, skin := range []string{"../../skins/black-and-wtf.yaml", "../../skins/high-contrast.yaml", "../../skins/monochrome.yaml", ""} {
		styles.Reset(false)
		if skin != "" {
			require.NoError(t, styles.Load(skin, false))
		}
		styles.Update()
		style := styles.Views().Yaml
		actual := colorizeYAML(style, "kind: [Pod]")
		assert.Equal(t, "["+style.KeyColor.String()+"::b]kind["+style.ColonColor.String()+"::-]: ["+style.ValueColor.String()+"::][Pod[]", actual, skin)
		if skin != "../../skins/black-and-wtf.yaml" {
			assert.Equal(t, styles.Semantic().Category, style.KeyColor, "semantic category follows the active skin")
			assert.Equal(t, styles.Semantic().Text, style.ValueColor, "values remain neutral and readable")
		}
	}
}
