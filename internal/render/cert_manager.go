// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package render

import (
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/certmanager"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/tcell/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var certificateHeader = model1.Header{
	{Name: "NAMESPACE"}, {Name: "NAME"}, {Name: "STATUS"},
	{Name: "EXPIRES"}, {Name: "RENEWAL"}, {Name: "ISSUER"}, {Name: "SECRET"},
	{Name: "DNS NAMES", Attrs: model1.Attrs{Wide: true}},
	{Name: "MESSAGE", Attrs: model1.Attrs{Wide: true}},
	{Name: "AGE", Attrs: model1.Attrs{Time: true, Wide: true}},
}

var certificateResourceHeader = model1.Header{
	{Name: "NAMESPACE"}, {Name: "NAME"}, {Name: "STATUS"}, {Name: "ISSUER"}, {Name: "MESSAGE"},
	{Name: "DNS NAMES", Attrs: model1.Attrs{Wide: true}},
	{Name: "AGE", Attrs: model1.Attrs{Time: true, Wide: true}},
}

// CertManager renders public controller status without reading TLS Secrets.
type CertManager struct {
	Base
	Resource string
}

func (c CertManager) baseHeader() model1.Header {
	if c.Resource == "certificates" {
		return certificateHeader
	}
	return certificateResourceHeader
}

func (c CertManager) Header(string) model1.Header { return c.doHeader(c.baseHeader()) }

func (c CertManager) Render(value any, _ string, row *model1.Row) error {
	o, ok := value.(*unstructured.Unstructured)
	if !ok || o == nil {
		return fmt.Errorf("expected Unstructured, got %T", value)
	}
	summary := certmanager.Summarize(o, time.Now())
	issuer := MissingValue
	if ref, ok := certmanager.Issuer(o); ok {
		kind := ref.Kind
		if ref.Group != "cert-manager.io" {
			kind += "." + ref.Group
		}
		issuer = kind + ":" + client.FQN(ref.Namespace, ref.Name)
	}
	dns, _, _ := unstructured.NestedStringSlice(o.Object, "spec", "dnsNames")
	if name, _, _ := unstructured.NestedString(o.Object, "spec", "dnsName"); name != "" {
		dns = append(dns, name)
	}
	secret, _, _ := unstructured.NestedString(o.Object, "spec", "secretName")
	values := map[string]string{
		"NAMESPACE": o.GetNamespace(), "NAME": o.GetName(), "STATUS": summary.State,
		"EXPIRES": certificateTimestamp(summary.NotAfter), "RENEWAL": certificateTimestamp(summary.RenewalTime),
		"ISSUER": issuer, "SECRET": certificateField(secret), "DNS NAMES": certificateField(strings.Join(dns, ",")),
		"MESSAGE": summary.Message, "AGE": ToAge(o.GetCreationTimestamp()),
	}
	row.ID = client.MetaFQN(&metav1.ObjectMeta{Namespace: o.GetNamespace(), Name: o.GetName()})
	header := c.baseHeader()
	row.Fields = make(model1.Fields, len(header))
	for i, col := range header {
		row.Fields[i] = values[col.Name]
	}
	if c.specs.isEmpty() {
		return nil
	}
	cols, err := c.specs.realize(o, header, row)
	cols.hydrateRow(row)
	return err
}

func certificateTimestamp(t time.Time) string {
	if t.IsZero() {
		return MissingValue
	}
	return t.UTC().Format(time.RFC3339)
}

func certificateField(value string) string {
	if value == "" {
		return MissingValue
	}
	return value
}

func (CertManager) ColorerFunc() model1.ColorerFunc {
	return func(ns string, h model1.Header, row *model1.RowEvent) tcell.Color {
		base := model1.DefaultColorer(ns, h, row)
		if row.Kind == model1.EventDelete || !model1.IsValid(ns, h, row.Row) {
			return base
		}
		idx, ok := h.IndexOf("STATUS", true)
		if !ok || idx >= len(row.Row.Fields) {
			return base
		}
		switch row.Row.Fields[idx] {
		case "Ready":
			return model1.CompletedColor
		case "Expired", "Not Ready", "Denied", "Failed", "Invalid", "Errored", "Revoked", "Invalid Request":
			return model1.ErrColor
		default:
			return model1.PendingColor
		}
	}
}
