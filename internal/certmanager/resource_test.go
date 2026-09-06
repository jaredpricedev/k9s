// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package certmanager

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func object(kind string, status map[string]any) *unstructured.Unstructured {
	group := certManagerGroup
	if kind == orderKind || kind == challengeKind {
		group = acmeGroup
	}
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": group + "/v1", "kind": kind, "metadata": map[string]any{"namespace": "apps", "generation": int64(2)}, "status": status}}
}

func condition(kind, status, reason string) any {
	return map[string]any{"type": kind, "status": status, "reason": reason, "observedGeneration": int64(2)}
}

func TestSupported(t *testing.T) {
	if Supported(nil) {
		t.Fatal("nil supported")
	}
	for _, resource := range []string{"certificates", "certificaterequests", "issuers", "clusterissuers"} {
		if !Supported(client.NewGVR("cert-manager.io/v1/" + resource)) {
			t.Error(resource)
		}
	}
	for _, resource := range []string{"orders", "challenges"} {
		if !Supported(client.NewGVR("acme.cert-manager.io/v1/" + resource)) {
			t.Error(resource)
		}
	}
	for _, gvr := range []string{"other.io/v1/certificates", "cert-manager.io/v1/secrets", "acme.cert-manager.io/v1/certificates", "v1/secrets", "cert-manager.io/v1/certificates:status"} {
		if Supported(client.NewGVR(gvr)) {
			t.Errorf("unsupported %s", gvr)
		}
	}
}

func TestCertificateLifetime(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name                   string
		before, after, renewal time.Time
		issuing                bool
		stale                  bool
		want                   string
	}{
		{"expired despite ready", now.Add(-24 * time.Hour), now.Add(-time.Second), time.Time{}, false, false, "Expired"},
		{"expiry boundary despite stale", now.Add(-24 * time.Hour), now, time.Time{}, false, true, "Expired"},
		{"short lived healthy", now.Add(-time.Hour), now.Add(time.Hour), time.Time{}, false, false, stateReady},
		{"before last tenth", now.Add(-89 * time.Minute), now.Add(11 * time.Minute), time.Time{}, false, false, stateReady},
		{"last tenth boundary", now.Add(-90 * time.Minute), now.Add(10 * time.Minute), time.Time{}, false, false, "Expiring"},
		{"renewal boundary", now.Add(-time.Hour), now.Add(time.Hour), now, false, false, "Renewal Due"},
		{"future renewal", now.Add(-time.Hour), now.Add(time.Hour), now.Add(time.Second), false, false, stateReady},
		{"renewing", now.Add(-time.Hour), now.Add(time.Hour), now.Add(-time.Minute), true, false, "Renewing"},
		{"initial issuing", time.Time{}, time.Time{}, time.Time{}, true, false, "Issuing"},
		{"stale ready", now.Add(-time.Hour), now.Add(time.Hour), time.Time{}, false, true, statePending},
		{"not yet valid", now.Add(time.Minute), now.Add(time.Hour), time.Time{}, false, false, "Not Yet Valid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ready := condition(stateReady, conditionTrue, stateReady).(map[string]any)
			if tc.stale {
				ready["observedGeneration"] = int64(1)
			}
			status := map[string]any{"conditions": []any{ready}}
			for key, ts := range map[string]time.Time{"notBefore": tc.before, "notAfter": tc.after, "renewalTime": tc.renewal} {
				if !ts.IsZero() {
					status[key] = ts.Format(time.RFC3339)
				}
			}
			if tc.issuing {
				status["conditions"] = append(status["conditions"].([]any), condition("Issuing", conditionTrue, "Issuing"))
			}
			s := Summarize(object(certificateKind, status), now)
			if s.State != tc.want {
				t.Errorf("state=%q want %q: %s", s.State, tc.want, s.Message)
			}
			if !s.NotAfter.Equal(tc.after) || !s.RenewalTime.Equal(tc.renewal) {
				t.Errorf("timestamps not preserved: %+v", s)
			}
		})
	}
}

func TestCertificateIncompleteValidity(t *testing.T) {
	now := time.Now().UTC()
	for _, fields := range []map[string]any{
		{}, {"notAfter": "bad"}, {"notAfter": int64(3)},
		{"notAfter": now.Add(time.Hour).Format(time.RFC3339)},
		{"notBefore": now.Add(time.Hour).Format(time.RFC3339), "notAfter": now.Add(time.Minute).Format(time.RFC3339)},
		{"notBefore": now.Add(-time.Hour).Format(time.RFC3339), "notAfter": now.Add(time.Hour).Format(time.RFC3339), "renewalTime": "bad"},
	} {
		fields["conditions"] = []any{condition(stateReady, conditionTrue, stateReady)}
		s := Summarize(object(certificateKind, fields), now)
		if s.State != stateUnknown || s.Message == "" {
			t.Errorf("invalid status summarized as %+v", s)
		}
	}
}

func TestCertificateIssuanceEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status map[string]any
		want   string
	}{
		{"revision proves prior issuance", map[string]any{"revision": int64(1), "conditions": []any{condition("Issuing", conditionTrue, "ManuallyTriggered")}}, "Renewing"},
		{"failed issuance", map[string]any{"conditions": []any{condition("Issuing", conditionFalse, stateFailed)}}, stateFailed},
		{"optional observed generation", map[string]any{"conditions": []any{map[string]any{"type": stateReady, "status": conditionTrue}}, "notBefore": "2026-09-06T11:00:00Z", "notAfter": "2026-09-06T13:00:00Z"}, stateReady},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
			if s := Summarize(object(certificateKind, tc.status), now); s.State != tc.want {
				t.Errorf("got %+v want %s", s, tc.want)
			}
		})
	}
}

func TestConditions(t *testing.T) {
	for _, tc := range []struct {
		kind       string
		conditions []any
		want       string
		reason     string
	}{
		{issuerKind, []any{condition(stateReady, conditionTrue, "Verified")}, stateReady, "Verified"},
		{clusterIssuerKind, []any{condition(stateReady, conditionFalse, "ErrInitIssuer")}, "Not Ready", "ErrInitIssuer"},
		{requestKind, []any{condition("Approved", conditionTrue, "Policy")}, "Approved", "Policy"},
		{requestKind, []any{condition("Approved", conditionTrue, "Policy"), condition(stateReady, conditionFalse, statePending)}, statePending, statePending},
		{requestKind, []any{condition(stateReady, conditionTrue, "Issued"), condition("Denied", conditionTrue, "Policy")}, "Denied", "Policy"},
		{requestKind, []any{condition("InvalidRequest", conditionTrue, "BadRequest")}, "Invalid Request", "BadRequest"},
		{requestKind, []any{condition(stateReady, conditionFalse, stateFailed)}, stateFailed, stateFailed},
		{certificateKind, []any{condition(stateReady, conditionFalse, "SecretMissing")}, "Not Ready", "SecretMissing"},
		{issuerKind, []any{condition(stateReady, stateUnknown, "Checking")}, stateUnknown, "Checking"},
	} {
		t.Run(tc.kind+tc.want, func(t *testing.T) {
			s := Summarize(object(tc.kind, map[string]any{"conditions": tc.conditions}), time.Now())
			if s.State != tc.want || !strings.Contains(s.Message, tc.reason) {
				t.Errorf("got %+v want %s/%s", s, tc.want, tc.reason)
			}
		})
	}
}

func TestACMEStates(t *testing.T) {
	for raw, want := range map[string]string{"valid": stateReady, "ready": "Ready to Finalize", "pending": statePending, "processing": stateProcessing, "invalid": "Invalid", "errored": "Errored", "expired": "Expired", "revoked": "Revoked", "mystery": stateUnknown} {
		for _, kind := range []string{orderKind, challengeKind} {
			s := Summarize(object(kind, map[string]any{"state": raw, "reason": "reported reason"}), time.Now())
			if s.State != want || !strings.Contains(s.Message, "reported reason") {
				t.Errorf("%s/%s: %+v", kind, raw, s)
			}
		}
	}
	s := Summarize(object(challengeKind, map[string]any{"processing": true, "presented": true, "reason": "Waiting for DNS propagation"}), time.Now())
	if s.State != stateProcessing || !strings.Contains(s.Message, "Waiting for DNS propagation") {
		t.Errorf("%+v", s)
	}
	terminal := Summarize(object(challengeKind, map[string]any{"processing": true, "state": "invalid"}), time.Now())
	if terminal.State != "Invalid" {
		t.Errorf("%+v", terminal)
	}
	processing := Summarize(object(challengeKind, map[string]any{"processing": true, "state": "pending", "reason": "Waiting for DNS propagation"}), time.Now())
	if processing.State != stateProcessing || !strings.Contains(processing.Message, "pending") {
		t.Errorf("%+v", processing)
	}
}

func TestStaleGenerationZero(t *testing.T) {
	ready := condition(stateReady, conditionTrue, stateReady).(map[string]any)
	ready["observedGeneration"] = int64(0)
	for _, kind := range []string{certificateKind, issuerKind, clusterIssuerKind} {
		if s := Summarize(object(kind, map[string]any{"conditions": []any{ready}}), time.Now()); s.State != statePending {
			t.Errorf("%s: %+v", kind, s)
		}
	}
}

func TestMalformedObjects(t *testing.T) {
	for _, o := range []*unstructured.Unstructured{nil, {}, {Object: map[string]any{"kind": int64(1), "metadata": "bad", "status": true}}, object(certificateKind, map[string]any{"conditions": []any{nil, "bad", map[string]any{"type": stateReady, "status": true}}})} {
		if s := Summarize(o, time.Now()); s.State != stateUnknown {
			t.Errorf("%+v", s)
		}
		if _, ok := Issuer(o); ok {
			t.Error("malformed issuer")
		}
		if len(References(o)) != 0 {
			t.Error("malformed refs")
		}
	}
}

func TestIssuerAndReferences(t *testing.T) {
	o := object(certificateKind, nil)
	o.Object["spec"] = map[string]any{"issuerRef": map[string]any{"name": "ca"}, "secretName": "tls"}
	issuer, ok := Issuer(o)
	if !ok || issuer != (Reference{Group: certManagerGroup, Kind: issuerKind, Namespace: "apps", Name: "ca"}) {
		t.Fatalf("%+v %v", issuer, ok)
	}
	o.Object["metadata"].(map[string]any)["ownerReferences"] = []any{
		map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "Ingress", "name": "ingress"},
		map[string]any{"apiVersion": "cert-manager.io/v1", "kind": requestKind, "name": "request"},
		map[string]any{"apiVersion": "cert-manager.io/v1", "kind": clusterIssuerKind, "name": "root"},
		map[string]any{"apiVersion": "evil.io/v1", "kind": certificateKind, "name": "foreign"},
		map[string]any{"apiVersion": "acme.cert-manager.io/v1", "kind": orderKind, "name": "order"},
		map[string]any{"apiVersion": "acme.cert-manager.io/v1", "kind": orderKind, "name": "order"},
		"malformed",
	}
	want := []Reference{issuer, {Kind: "Secret", Namespace: "apps", Name: "tls"}, {Group: certManagerGroup, Kind: requestKind, Namespace: "apps", Name: "request"}, {Group: certManagerGroup, Kind: clusterIssuerKind, Name: "root"}, {Group: acmeGroup, Kind: orderKind, Namespace: "apps", Name: "order"}}
	if got := References(o); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v want %+v", got, want)
	}
	o.Object["spec"].(map[string]any)["issuerRef"] = map[string]any{"name": "root", "kind": clusterIssuerKind}
	if ref, _ := Issuer(o); ref.Namespace != "" {
		t.Errorf("%+v", ref)
	}
	o.Object["spec"].(map[string]any)["issuerRef"] = map[string]any{"name": "external", "group": "pki.example.com", "kind": "ExternalIssuer"}
	if ref, _ := Issuer(o); ref.Group != "pki.example.com" || ref.Kind != "ExternalIssuer" || ref.Namespace != "apps" {
		t.Errorf("%+v", ref)
	}
	o.Object["spec"].(map[string]any)["issuerRef"] = map[string]any{"name": "external", "group": "pki.example.com", "kind": clusterIssuerKind}
	if ref, _ := Issuer(o); ref.Namespace != "apps" {
		t.Errorf("custom group kind must retain source namespace for discovery: %+v", ref)
	}
}

func TestValidityWarningRetainsControllerMessage(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, want string
		after      time.Time
		conditions []any
	}{
		{"expired renewing", "Expired", now, []any{map[string]any{"type": "Issuing", "status": conditionTrue, "reason": "Renewing", "message": "Waiting for DNS propagation"}}},
		{"expiring ready", "Expiring", now.Add(10 * time.Minute), []any{map[string]any{"type": stateReady, "status": conditionTrue, "reason": stateReady, "message": "Certificate is ready"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status := map[string]any{"notBefore": now.Add(-90 * time.Minute).Format(time.RFC3339), "notAfter": tc.after.Format(time.RFC3339), "conditions": tc.conditions}
			s := Summarize(object(certificateKind, status), now)
			detail := tc.conditions[0].(map[string]any)["message"].(string)
			if s.State != tc.want || !strings.Contains(s.Message, detail) || !strings.Contains(s.Message, tc.after.Format(time.RFC3339)) {
				t.Errorf("%+v", s)
			}
		})
	}
}

func TestMalformedObservedGeneration(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	for _, generation := range []any{"2", true, nil, float64(2), int64(-1)} {
		for _, kind := range []string{certificateKind, issuerKind, clusterIssuerKind} {
			c := condition(stateReady, conditionTrue, stateReady).(map[string]any)
			c["observedGeneration"] = generation
			status := map[string]any{"conditions": []any{c}, "notBefore": "2026-09-06T11:00:00Z", "notAfter": "2026-09-06T13:00:00Z"}
			if s := Summarize(object(kind, status), now); s.State != stateUnknown || !strings.Contains(s.Message, "observedGeneration") {
				t.Errorf("%s/%v: %+v", kind, generation, s)
			}
		}
	}
	issuing := condition("Issuing", conditionTrue, "Issuing").(map[string]any)
	issuing["observedGeneration"] = "2"
	if s := Summarize(object(certificateKind, map[string]any{"conditions": []any{issuing}}), now); s.State != stateUnknown {
		t.Errorf("invalid Issuing condition: %+v", s)
	}
}

func TestStaleIssuanceFailure(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	for _, withReady := range []bool{false, true} {
		issuing := condition("Issuing", conditionFalse, stateFailed).(map[string]any)
		issuing["observedGeneration"] = int64(1)
		conditions := []any{issuing}
		if withReady {
			conditions = append(conditions, condition(stateReady, conditionTrue, stateReady))
		}
		status := map[string]any{
			"conditions": conditions,
			"notBefore":  now.Add(-time.Hour).Format(time.RFC3339),
			"notAfter":   now.Add(time.Hour).Format(time.RFC3339),
		}
		if s := Summarize(object(certificateKind, status), now); s.State != statePending {
			t.Errorf("current Ready present=%v: %+v", withReady, s)
		}
		status["notAfter"] = now.Format(time.RFC3339)
		if s := Summarize(object(certificateKind, status), now); s.State != "Expired" {
			t.Errorf("expiry must still win: %+v", s)
		}
	}
}
