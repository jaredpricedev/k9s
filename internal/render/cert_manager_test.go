// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package render

import (
	"testing"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestCertManagerRenderExpiredCertificateAndCustomColumns(t *testing.T) {
	r := CertManager{Resource: "certificates"}
	o := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cert-manager.io/v1", "kind": "Certificate",
		"metadata": map[string]any{"name": "api-tls", "namespace": "apps", "generation": int64(1)},
		"spec":     map[string]any{"secretName": "api-tls", "issuerRef": map[string]any{"name": "internal-ca", "kind": "ClusterIssuer"}, "dnsNames": []any{"api.example.test", "api.internal.test"}},
		"status":   map[string]any{"notBefore": "2000-01-01T00:00:00Z", "notAfter": "2000-04-01T00:00:00Z", "renewalTime": "2000-03-01T00:00:00Z", "conditions": []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": int64(1)}}},
	}}
	var row model1.Row
	require.NoError(t, r.Render(o, "apps", &row))
	assert.Equal(t, "apps/api-tls", row.ID)
	assert.Contains(t, row.Fields, "Expired")
	assert.Contains(t, row.Fields, "2000-04-01T00:00:00Z")
	assert.Contains(t, row.Fields, "2000-03-01T00:00:00Z")
	assert.Contains(t, row.Fields, "ClusterIssuer:internal-ca")
	assert.Contains(t, row.Fields, "api.example.test,api.internal.test")
	assert.Equal(t, []string{"NAMESPACE", "NAME", "STATUS", "EXPIRES", "RENEWAL", "ISSUER", "SECRET"}, r.Header("apps").ColumnNames(false))
	r.SetViewSetting(&config.ViewSetting{Columns: []string{"NAME", "ISSUER", "STATUS"}})
	require.NoError(t, r.Render(o, "apps", &row))
	assert.Equal(t, []string{"NAME", "ISSUER", "STATUS"}, r.Header("apps").ColumnNames(true)[:3])
	assert.Equal(t, model1.Fields{"api-tls", "ClusterIssuer:internal-ca", "Expired"}, row.Fields[:3])
}

func TestCertManagerRenderClusterIssuerAndInvalidValues(t *testing.T) {
	r := CertManager{Resource: "clusterissuers"}
	o := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "cert-manager.io/v1", "kind": "ClusterIssuer", "metadata": map[string]any{"name": "vault"}}}
	var row model1.Row
	require.NoError(t, r.Render(o, "", &row))
	assert.Equal(t, "-/vault", row.ID)
	assert.NotContains(t, r.Header("").ColumnNames(false), "EXPIRES")
	require.Error(t, r.Render("bad", "", &row))
	require.Error(t, r.Render((*unstructured.Unstructured)(nil), "", &row))
}

func TestCertManagerStatusColorsRespectCustomColumnsAndDeletion(t *testing.T) {
	previous := []tcell.Color{model1.ErrColor, model1.PendingColor, model1.CompletedColor, model1.KillColor}
	t.Cleanup(func() {
		model1.ErrColor, model1.PendingColor, model1.CompletedColor, model1.KillColor = previous[0], previous[1], previous[2], previous[3]
	})
	model1.ErrColor, model1.PendingColor, model1.CompletedColor, model1.KillColor = tcell.ColorRed, tcell.ColorYellow, tcell.ColorGreen, tcell.ColorGray
	h := model1.Header{{Name: "STATUS"}, {Name: "NAME"}}
	colorer := (CertManager{}).ColorerFunc()
	for _, tt := range []struct {
		state string
		want  tcell.Color
	}{{"Expired", tcell.ColorRed}, {"Denied", tcell.ColorRed}, {"Renewal Due", tcell.ColorYellow}, {"Unknown", tcell.ColorYellow}, {"Ready", tcell.ColorGreen}} {
		row := model1.NewRowEvent(model1.EventUnchanged, model1.Row{Fields: model1.Fields{tt.state, "tls"}})
		assert.Equal(t, tt.want, colorer("", h, &row), tt.state)
		row.Kind = model1.EventDelete
		assert.Equal(t, tcell.ColorGray, colorer("", h, &row))
	}
	assert.NotPanics(t, func() { colorer("", h, &model1.RowEvent{}) })
}
