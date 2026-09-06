// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

// Package certmanager interprets cert-manager resource metadata and status.
// It does not fetch referenced objects or inspect certificate or private key data.
package certmanager

import (
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	certManagerGroup  = "cert-manager.io"
	acmeGroup         = "acme.cert-manager.io"
	certificateKind   = "Certificate"
	requestKind       = "CertificateRequest"
	issuerKind        = "Issuer"
	clusterIssuerKind = "ClusterIssuer"
	orderKind         = "Order"
	challengeKind     = "Challenge"
	conditionTrue     = "True"
	conditionFalse    = "False"
	stateReady        = "Ready"
	stateUnknown      = "Unknown"
	statePending      = "Pending"
	stateProcessing   = "Processing"
	stateFailed       = "Failed"
)

// Summary describes the currently reported health of a resource. Zero timestamps
// mean the corresponding timestamp is unavailable or invalid.
type Summary struct {
	State, Message        string
	NotAfter, RenewalTime time.Time
}

// Reference identifies a related object without retrieving its contents.
type Reference struct {
	Group, Kind, Namespace, Name string
}

// Supported limits cert-manager behavior to its known resource types.
func Supported(gvr *client.GVR) bool {
	if gvr == nil || gvr.SubResource() != "" {
		return false
	}
	switch gvr.G() {
	case certManagerGroup:
		switch gvr.R() {
		case "certificates", "certificaterequests", "issuers", "clusterissuers":
			return true
		}
	case acmeGroup:
		switch gvr.R() {
		case "orders", "challenges":
			return true
		}
	}
	return false
}

// Summarize interprets status at now without assuming that a stale Ready
// condition proves a certificate is still valid. The expiring warning covers the
// final tenth of the reported certificate lifetime, including the boundary.
func Summarize(o *unstructured.Unstructured, now time.Time) Summary {
	if o == nil {
		return Summary{State: stateUnknown, Message: "Status not reported"}
	}
	switch o.GetKind() {
	case certificateKind:
		return certificateSummary(o, now)
	case orderKind, challengeKind:
		return acmeSummary(o)
	case requestKind:
		for _, state := range []struct{ condition, label string }{{"Denied", "Denied"}, {"InvalidRequest", "Invalid Request"}, {stateFailed, stateFailed}} {
			if c := findCondition(o, state.condition); c.status == conditionTrue {
				return Summary{State: state.label, Message: c.message()}
			}
		}
		if c := findCondition(o, stateReady); c.status != "" {
			return readySummary(o, c)
		}
		if c := findCondition(o, "Approved"); c.status == conditionTrue {
			return Summary{State: "Approved", Message: c.message()}
		}
	case issuerKind, clusterIssuerKind:
		return readySummary(o, findCondition(o, stateReady))
	}
	return Summary{State: stateUnknown, Message: "Status not reported"}
}

func certificateSummary(o *unstructured.Unstructured, now time.Time) Summary {
	after, badAfter := statusTime(o, "notAfter")
	before, badBefore := statusTime(o, "notBefore")
	renewal, badRenewal := statusTime(o, "renewalTime")
	issuing, ready := findCondition(o, "Issuing"), findCondition(o, stateReady)
	detail := ready.message()
	if issuing.status != "" && issuing.message() != "" {
		detail = joinMessage(issuing.message(), detail)
	}
	result := func(state, message string) Summary {
		return Summary{State: state, Message: message, NotAfter: after, RenewalTime: renewal}
	}
	if !after.IsZero() && !now.Before(after) {
		return result("Expired", joinMessage("Certificate expired at "+after.Format(time.RFC3339), detail))
	}
	if issuing.invalidGeneration || ready.invalidGeneration {
		return result(stateUnknown, joinMessage("Condition observedGeneration is invalid", detail))
	}
	if issuing.status == conditionTrue && !issuing.stale(o) {
		revision, _, _ := unstructured.NestedInt64(o.Object, "status", "revision")
		if !after.IsZero() || revision > 0 {
			return result("Renewing", issuing.message())
		}
		return result("Issuing", issuing.message())
	}
	// An old issuance failure cannot describe the current generation, even when
	// Ready is current. Wait for both reported conditions to catch up.
	if ready.stale(o) || issuing.stale(o) {
		return result(statePending, "Waiting for status to observe the current generation")
	}
	if issuing.status == conditionFalse && issuing.reason == stateFailed {
		return result(stateFailed, issuing.message())
	}
	if ready.status != conditionTrue {
		s := readySummary(o, ready)
		return result(s.State, s.Message)
	}
	if badAfter || badBefore || after.IsZero() || before.IsZero() || !before.Before(after) {
		return result(stateUnknown, joinMessage("Certificate validity timestamps are missing or invalid", ready.message()))
	}
	if now.Before(before) {
		return result("Not Yet Valid", "Certificate becomes valid at "+before.Format(time.RFC3339))
	}
	if after.Sub(now) <= after.Sub(before)/10 {
		return result("Expiring", joinMessage("Certificate is in the final 10% of its lifetime; expires at "+after.Format(time.RFC3339), detail))
	}
	if !renewal.IsZero() && !now.Before(renewal) {
		return result("Renewal Due", "Scheduled renewal time reached: "+renewal.Format(time.RFC3339))
	}
	if badRenewal {
		return result(stateUnknown, joinMessage("Renewal timestamp is invalid", ready.message()))
	}
	return result(stateReady, ready.message())
}

type resourceCondition struct {
	status, reason, detail string
	observedGeneration     int64
	hasGeneration          bool
	invalidGeneration      bool
}

func findCondition(o *unstructured.Unstructured, kind string) resourceCondition {
	value, _, _ := unstructured.NestedFieldNoCopy(o.Object, "status", "conditions")
	conditions, _ := value.([]any)
	for _, value := range conditions {
		c, ok := value.(map[string]any)
		if !ok || stringField(c, "type") != kind {
			continue
		}
		generation, present, err := unstructured.NestedInt64(c, "observedGeneration")
		return resourceCondition{
			status: stringField(c, "status"), reason: stringField(c, "reason"), detail: stringField(c, "message"),
			observedGeneration: generation, hasGeneration: present && err == nil, invalidGeneration: err != nil || generation < 0,
		}
	}
	return resourceCondition{}
}

func (c resourceCondition) stale(o *unstructured.Unstructured) bool {
	return c.hasGeneration && c.observedGeneration < o.GetGeneration()
}

func (c resourceCondition) message() string { return joinMessage(c.reason, c.detail) }

func readySummary(o *unstructured.Unstructured, c resourceCondition) Summary {
	if c.invalidGeneration {
		return Summary{State: stateUnknown, Message: joinMessage("Condition observedGeneration is invalid", c.message())}
	}
	if c.stale(o) {
		return Summary{State: statePending, Message: "Waiting for status to observe the current generation"}
	}
	state := stateUnknown
	switch c.status {
	case conditionTrue:
		state = stateReady
	case conditionFalse:
		state = "Not Ready"
		switch c.reason {
		case stateFailed:
			state = stateFailed
		case statePending:
			state = statePending
		}
	}
	message := c.message()
	if message == "" {
		message = "Ready condition is " + c.status
		if c.status == "" {
			message = "Ready condition not reported"
		}
	}
	return Summary{State: state, Message: message}
}

func acmeSummary(o *unstructured.Unstructured) Summary {
	raw := stringField(o.Object, "status", "state")
	state := stateUnknown
	switch raw {
	case "valid":
		state = stateReady
	case "ready":
		state = "Ready to Finalize"
	case "pending":
		state = statePending
	case "processing":
		state = stateProcessing
	case "invalid":
		state = "Invalid"
	case "errored":
		state = "Errored"
	case "expired":
		state = "Expired"
	case "revoked":
		state = "Revoked"
	}
	message := "ACME state not reported"
	if raw != "" {
		message = "ACME state: " + raw
	}
	if raw == "" || raw == "pending" {
		processing, _, _ := unstructured.NestedBool(o.Object, "status", "processing")
		if processing {
			state = stateProcessing
			if raw == "" {
				message = "Challenge is scheduled for processing"
			}
		}
	}
	return Summary{State: state, Message: joinMessage(message, stringField(o.Object, "status", "reason"))}
}

func statusTime(o *unstructured.Unstructured, field string) (time.Time, bool) {
	raw, present, err := unstructured.NestedString(o.Object, "status", field)
	if err != nil {
		return time.Time{}, true
	}
	if !present {
		return time.Time{}, false
	}
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil || ts.IsZero() {
		return time.Time{}, true
	}
	return ts, false
}

func stringField(object map[string]any, fields ...string) string {
	value, _, _ := unstructured.NestedString(object, fields...)
	return value
}

func joinMessage(first, second string) string {
	if first == "" {
		return second
	}
	if second == "" || first == second {
		return first
	}
	return first + ": " + second
}

// Issuer returns the issuerRef using cert-manager's group/kind defaults.
// External issuer kinds retain the source namespace; discovery must determine
// whether those custom resources are cluster-scoped before navigation.
func Issuer(o *unstructured.Unstructured) (Reference, bool) {
	if o == nil {
		return Reference{}, false
	}
	name := stringField(o.Object, "spec", "issuerRef", "name")
	if strings.TrimSpace(name) == "" {
		return Reference{}, false
	}
	group, _, groupErr := unstructured.NestedString(o.Object, "spec", "issuerRef", "group")
	kind, _, kindErr := unstructured.NestedString(o.Object, "spec", "issuerRef", "kind")
	if groupErr != nil || kindErr != nil {
		return Reference{}, false
	}
	if group == "" {
		group = certManagerGroup
	}
	if kind == "" {
		kind = issuerKind
	}
	namespace := o.GetNamespace()
	if group == certManagerGroup && kind == clusterIssuerKind {
		namespace = ""
	}
	return Reference{Group: group, Kind: kind, Namespace: namespace, Name: name}, true
}

// References returns issuer, target Secret, and supported cert-manager/ACME
// owners in that order, deduplicated. Arbitrary owner kinds are not navigable.
func References(o *unstructured.Unstructured) []Reference {
	if o == nil {
		return nil
	}
	var refs []Reference
	seen := make(map[Reference]bool)
	add := func(ref Reference) {
		if ref.Name != "" && !seen[ref] {
			seen[ref] = true
			refs = append(refs, ref)
		}
	}
	if issuer, ok := Issuer(o); ok {
		add(issuer)
	}
	if o.GetKind() == certificateKind {
		add(Reference{Kind: "Secret", Namespace: o.GetNamespace(), Name: stringField(o.Object, "spec", "secretName")})
	}
	value, _, _ := unstructured.NestedFieldNoCopy(o.Object, "metadata", "ownerReferences")
	owners, _ := value.([]any)
	for _, value := range owners {
		owner, ok := value.(map[string]any)
		if !ok {
			continue
		}
		gv, err := schema.ParseGroupVersion(stringField(owner, "apiVersion"))
		kind := stringField(owner, "kind")
		if err != nil || !supportedOwner(gv.Group, kind) {
			continue
		}
		namespace := o.GetNamespace()
		if kind == clusterIssuerKind {
			namespace = ""
		}
		add(Reference{Group: gv.Group, Kind: kind, Namespace: namespace, Name: stringField(owner, "name")})
	}
	return refs
}

func supportedOwner(group, kind string) bool {
	switch group + "/" + kind {
	case "cert-manager.io/Certificate", "cert-manager.io/CertificateRequest", "cert-manager.io/Issuer", "cert-manager.io/ClusterIssuer",
		"acme.cert-manager.io/Order", "acme.cert-manager.io/Challenge":
		return true
	}
	return false
}
