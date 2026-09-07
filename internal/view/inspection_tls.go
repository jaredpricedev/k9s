// SPDX-License-Identifier: Apache-2.0
package view

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/certmanager"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/inspect"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	inspectionSecretKind      = "Secret"
	inspectionCertificateKind = "Certificate"
)

const inspectionSecretsResource = "secrets"

func tlsEntryResource(resource string) bool {
	switch resource {
	case inspectionSecretsResource, "certificates", "ingresses", "gateways":
		return true
	}
	return false
}
func tlsSecretReferences(o *unstructured.Unstructured) []certmanager.Reference {
	var refs []certmanager.Reference
	add := func(name, ns string) {
		if name != "" {
			if ns == "" {
				ns = o.GetNamespace()
			}
			refs = append(refs, certmanager.Reference{Kind: inspectionSecretKind, Name: name, Namespace: ns})
		}
	}
	switch o.GetKind() {
	case inspectionCertificateKind:
		add(nestedText(o.Object, "spec", "secretName"), "")
	case "Ingress":
		items, _, _ := unstructured.NestedSlice(o.Object, "spec", "tls")
		for _, item := range items {
			if m, ok := item.(map[string]any); ok {
				add(nestedText(m, "secretName"), "")
			}
		}
	case "Gateway":
		listeners, _, _ := unstructured.NestedSlice(o.Object, "spec", "listeners")
		for _, l := range listeners {
			if m, ok := l.(map[string]any); ok {
				items, _, _ := unstructured.NestedSlice(m, "tls", "certificateRefs")
				for _, item := range items {
					if ref, ok := item.(map[string]any); ok {
						kind, group := nestedText(ref, "kind"), nestedText(ref, "group")
						if group == "" && (kind == "" || kind == inspectionSecretKind) {
							add(nestedText(ref, "name"), nestedText(ref, "namespace"))
						}
					}
				}
			}
		}
	case "Pod":
		volumes, _, _ := unstructured.NestedSlice(o.Object, "spec", "volumes")
		for _, v := range volumes {
			if m, ok := v.(map[string]any); ok {
				add(nestedText(m, "secret", "secretName"), "")
				sources, _, _ := unstructured.NestedSlice(m, "projected", "sources")
				for _, source := range sources {
					if projection, ok := source.(map[string]any); ok {
						add(nestedText(projection, "secret", "name"), "")
					}
				}
			}
		}
		for _, kind := range []string{"containers", "initContainers"} {
			containers, _, _ := unstructured.NestedSlice(o.Object, "spec", kind)
			for _, c := range containers {
				if m, ok := c.(map[string]any); ok {
					envs, _, _ := unstructured.NestedSlice(m, "env")
					for _, e := range envs {
						if env, ok := e.(map[string]any); ok {
							add(nestedText(env, "valueFrom", "secretKeyRef", "name"), "")
						}
					}
					envs, _, _ = unstructured.NestedSlice(m, "envFrom")
					for _, e := range envs {
						if env, ok := e.(map[string]any); ok {
							add(nestedText(env, "secretRef", "name"), "")
						}
					}
				}
			}
		}
	}
	seen := map[string]bool{}
	var unique []certmanager.Reference
	for _, ref := range refs {
		k := ref.Namespace + "/" + ref.Name
		if !seen[k] {
			unique = append(unique, ref)
			seen[k] = true
		}
	}
	return unique
}

func tlsResourceReport(ctx context.Context, conn client.Connection, o *unstructured.Unstructured) (string, error) {
	if o.GetKind() == inspectionSecretKind {
		data, err := secretCertificate(o)
		if err != nil {
			return "", err
		}
		report, err := inspect.Certificates(data, time.Now())
		if err != nil {
			return "", err
		}
		return report + "\nUse g for same-namespace configuration references. References do not prove the certificate is served.\n" +
			"Offline trust check from the resource list: :tlsverify hostname [ca=/absolute/path]\n" +
			"Explicit network check: :tlsprobe host:port serverName [ca=/absolute/path]\n", nil
	}
	refs := tlsSecretReferences(o)
	if len(refs) == 0 {
		return "", fmt.Errorf("no supported TLS Secret references; select a Secret, Certificate, Ingress or Gateway")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "TLS CONFIGURATION REFERENCES\n%s %s/%s\nUse g to jump to referenced resources.\n", o.GetKind(), o.GetNamespace(), o.GetName())
	dyn, err := conn.DynDial()
	if err != nil {
		return "", err
	}
	for i, ref := range refs {
		if i >= 20 {
			b.WriteString("Certificate inspection truncated at 20 references; use g for individual resources.\n")
			break
		}
		fmt.Fprintf(&b, "\nSECRET %s/%s\n", ref.Namespace, ref.Name)
		if ref.Namespace != o.GetNamespace() {
			b.WriteString("Cross-namespace reference: not fetched by this inspector. Check ReferenceGrant and controller status.\n")
			continue
		}
		secret, err := dyn.Resource(schema.GroupVersionResource{Version: "v1", Resource: inspectionSecretsResource}).
			Namespace(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil {
			fmt.Fprintf(&b, "Unavailable: %v\n", err)
			continue
		}
		data, err := secretCertificate(secret)
		if err != nil {
			fmt.Fprintf(&b, "Unavailable: %v\n", err)
			continue
		}
		report, err := inspect.Certificates(data, time.Now())
		if err != nil {
			fmt.Fprintf(&b, "Unavailable: %v\n", err)
			continue
		}
		b.WriteString(report)
	}
	b.WriteString("Configuration only; controller acceptance and live served certificate are not established.\n")
	return b.String(), nil
}
func secretCertificate(o *unstructured.Unstructured) ([]byte, error) {
	encoded, _, _ := unstructured.NestedString(o.Object, "data", "tls.crt")
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("tls.crt is not valid base64")
	}
	return data, nil
}
func tlsConsumers(ctx context.Context, conn client.Connection, o *unstructured.Unstructured) []inspectionReference {
	dyn, err := conn.DynDial()
	if err != nil {
		return []inspectionReference{{notice: err.Error()}}
	}
	var refs []inspectionReference
	types := []struct {
		gvr  schema.GroupVersionResource
		kind string
	}{
		{schema.GroupVersionResource{Version: "v1", Resource: "pods"}, "Pod"},
		{schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}, "Ingress"},
		{schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}, inspectionCertificateKind},
		{schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}, "Gateway"},
	}
	for _, typ := range types {
		list, err := dyn.Resource(typ.gvr).Namespace(o.GetNamespace()).List(ctx, metav1.ListOptions{Limit: 200})
		if err != nil {
			refs = append(refs, inspectionReference{notice: typ.kind + " visibility unavailable: " + err.Error()})
			continue
		}
		for i := range list.Items {
			item := &list.Items[i]
			item.SetKind(typ.kind)
			for _, ref := range tlsSecretReferences(item) {
				if ref.Name == o.GetName() && ref.Namespace == o.GetNamespace() {
					refs = append(refs, inspectionReference{ref: certmanager.Reference{Group: typ.gvr.Group, Kind: typ.kind, Name: item.GetName(), Namespace: item.GetNamespace()}})
					break
				}
			}
		}
		if list.GetContinue() != "" {
			refs = append(refs, inspectionReference{notice: typ.kind + " scan truncated at 200"})
		}
	}
	refs = append(refs, inspectionReference{notice: "Same-namespace configuration references only; mounted certificates may be stale. " +
		"Cross-namespace consumers are not scanned."})
	return refs
}

func (c *Command) tlsCheckCommand(line string) {
	parts := strings.Fields(line)
	probe := len(parts) > 0 && parts[0] == "tlsprobe"
	minimum := 2
	if probe {
		minimum = 3
	}
	if len(parts) < minimum || len(parts) > minimum+1 {
		c.app.Flash().Err(fmt.Errorf("use :tlsverify hostname [ca=/absolute/path] or :tlsprobe host:port serverName [ca=/absolute/path]"))
		return
	}
	ca := ""
	if len(parts) > minimum {
		if !strings.HasPrefix(parts[minimum], "ca=") || len(parts[minimum]) == 3 {
			c.app.Flash().Err(fmt.Errorf("expected ca=/absolute/path"))
			return
		}
		ca = strings.TrimPrefix(parts[minimum], "ca=")
	}
	d := &inspectionDetails{Details: NewDetails(c.app, "TLS verification",
		strings.Join(parts[1:minimum], " "), contentInspection, true).Update("Loading TLS verification...")}
	var (
		conn client.Connection
		path string
	)
	if !probe {
		switch v := c.app.Content.Top().(type) {
		case ResourceViewer:
			if v.GVR().R() == inspectionSecretsResource {
				path = v.GetTable().GetSelectedItem()
			}
		case *inspectionDetails:
			if v.contextName == c.app.Config.ActiveContextName() {
				path = v.secretPath
			}
		}
		if path == "" {
			c.app.Flash().Err(fmt.Errorf("select a Secret for offline verification"))
			return
		}
		var err error
		conn, err = pinInspectionConnection(c.app.Conn())
		if err != nil {
			c.app.Flash().Err(err)
			return
		}
	}
	d.loader = func(ctx context.Context) (string, error) {
		roots, source, err := inspect.TrustRoots(ca)
		if err != nil {
			return "", err
		}
		preface := "TRUST SOURCE\n" + source + "\n\n"
		if probe {
			report, probeErr := inspect.Probe(ctx, parts[1], parts[2], roots)
			if probeErr != nil {
				return preface + "TLS probe failed from the k9plus machine: " + probeErr.Error(), nil
			}
			return preface + report, nil
		}
		dyn, err := conn.DynDial()
		if err != nil {
			return "", err
		}
		ns, n := client.Namespaced(path)
		obj, err := dyn.Resource(schema.GroupVersionResource{Version: "v1", Resource: inspectionSecretsResource}).Namespace(ns).Get(ctx, n, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		data, err := secretCertificate(obj)
		if err != nil {
			return "", err
		}
		report, err := inspect.VerifyBundle(data, roots, parts[1], time.Now())
		if err != nil {
			return preface + "VERIFICATION FAILED\n" + err.Error() + "\nNo endpoint contacted. Revocation not checked.", nil
		}
		return preface + report, nil
	}
	if err := c.app.inject(d, false); err != nil {
		c.app.Flash().Err(err)
		return
	}
	d.refresh()
}
