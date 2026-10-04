// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
// Package operator contains explicit semantic adapters, independent of the UI.
package operator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/derailed/k9s/internal/provider"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
)

const (
	Group               = "cert-manager.io"
	Version             = "v1"
	CertificateKind     = "Certificate"
	CertificateResource = "certificates"
	CertificateGVR      = "cert-manager.io/v1/certificates"
	MaxConditions       = 128
	MaxFacts            = 32
	MaxFieldBytes       = 256
	MaxReads            = 2
	ReadTimeout         = 3 * time.Second
	CollectionTimeout   = 8 * time.Second
	Complete            = "observed"
	Unknown             = "unknown"
	Stale               = "stale"
	Unsupported         = "unsupported semantics"
)

const (
	ConditionReady    = "Ready"
	ConditionIssuing  = "Issuing"
	ConditionTrue     = "True"
	ConditionFalse    = "False"
	ConditionUnknown  = "Unknown"
	IssuerKind        = "Issuer"
	ClusterIssuerKind = "ClusterIssuer"
	ControllerReport  = "controller report"
	Unreported        = "Unreported"
)

var Tabs = []string{"Overview", "Conditions", "Relations", "Sources"}

type Identity struct {
	Context, GVR, Kind, Namespace, Name, UID, ResourceVersion string
	Generation                                                *int64
}
type Coverage struct {
	Source, State string
	ReadAt        time.Time
}
type Condition struct {
	Type, Status, Meaning string
	ObservedGeneration    *int64
	TransitionTime        *time.Time
}
type Reference struct {
	Group, Kind, Namespace, Name string
	Defaulted                    bool
}
type Relation struct {
	Declared Reference
	Observed *Identity
	Coverage Coverage
	// issuerRef carries no UID. A named GET never proves a UID-backed source link.
	UIDLinkVerified bool
}
type Action struct{ Kind, Label string }
type Report struct {
	Scope                               provider.Scope
	CapturedAt                          time.Time
	Identity                            Identity
	Conditions                          []Condition
	NotBefore, NotAfter, RenewalTime    *time.Time
	Revision                            *int64
	SecretReference                     string
	Issuer                              Relation
	Coverage                            []Coverage
	SchemaIssues, UnsupportedConditions int
	CustomJump                          bool
	Actions                             []Action
}

func Supports(gvr string) bool { return gvr == CertificateGVR }
func safe(value string) string {
	if len(value) > MaxFieldBytes {
		value = strings.ToValidUTF8(value[:MaxFieldBytes], "")
	}
	return strings.Clone(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, value))
}

// ValidScope requires one bounded, exact captured Certificate identity.
func ValidScope(scope *provider.Scope) bool {
	return scope != nil && Supports(scope.GVR) && scope.Context != "" && safe(scope.Context) == scope.Context &&
		scope.UID != "" && safe(scope.UID) == scope.UID &&
		len(validation.IsDNS1123Label(scope.TargetNamespace)) == 0 && len(validation.IsDNS1123Subdomain(scope.Name)) == 0
}
func boundedScope(scope *provider.Scope) provider.Scope {
	if scope == nil {
		return provider.Scope{}
	}
	result := *scope
	result.Context, result.Namespace, result.TargetNamespace = safe(scope.Context), safe(scope.Namespace), safe(scope.TargetNamespace)
	result.GVR, result.Name, result.UID = safe(scope.GVR), safe(scope.Name), safe(scope.UID)
	if result.UID != scope.UID {
		result.UID = ""
	}
	return result
}

func str(obj *unstructured.Unstructured, path ...string) string {
	v, _, _ := unstructured.NestedString(obj.Object, path...)
	return safe(v)
}
func integer(obj *unstructured.Unstructured, path ...string) *int64 {
	value, found, err := unstructured.NestedInt64(obj.Object, path...)
	if err != nil || !found || value < 1 {
		return nil
	}
	return &value
}
func timestamp(obj *unstructured.Unstructured, path ...string) *time.Time {
	value, found, err := unstructured.NestedString(obj.Object, path...)
	if !found || err != nil || len(value) > MaxFieldBytes {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil
	}
	return &parsed
}
func identity(scope *provider.Scope, gvr schema.GroupVersionResource, obj *unstructured.Unstructured) Identity {
	return Identity{Context: safe(scope.Context), GVR: gvr.Group + "/" + gvr.Version + "/" + gvr.Resource, Kind: obj.GetKind(),
		Namespace: obj.GetNamespace(), Name: obj.GetName(), UID: safe(string(obj.GetUID())), ResourceVersion: safe(obj.GetResourceVersion()),
		Generation: integer(obj, "metadata", "generation")}
}
func validObject(obj *unstructured.Unstructured, gvr schema.GroupVersionResource, kind, namespace, name string) bool {
	return obj != nil && obj.GetAPIVersion() == gvr.Group+"/"+gvr.Version && obj.GetKind() == kind &&
		obj.GetNamespace() == namespace && obj.GetName() == name && obj.GetUID() != "" && len(obj.GetUID()) <= MaxFieldBytes &&
		len(obj.GetResourceVersion()) <= MaxFieldBytes &&
		safe(string(obj.GetUID())) == string(obj.GetUID()) && safe(obj.GetResourceVersion()) == obj.GetResourceVersion()
}

// NamedAbsent accepts only a structured object-specific Kubernetes 404.
func NamedAbsent(err error, gvr schema.GroupVersionResource, name string) bool {
	if apierrors.IsUnexpectedServerError(err) {
		return false
	}
	var status apierrors.APIStatus
	if !errors.As(err, &status) {
		return false
	}
	value := status.Status()
	return value.Code == 404 && value.Reason == metav1.StatusReasonNotFound && value.Details != nil &&
		value.Details.Group == gvr.Group && value.Details.Kind == gvr.Resource && value.Details.Name == name
}
func read(ctx context.Context, reader dynamic.Interface, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, Coverage) {
	readCtx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()
	obj, err := reader.Resource(gvr).Namespace(namespace).Get(readCtx, name, metav1.GetOptions{})
	coverage := Coverage{Source: safe(gvr.Group + "/" + gvr.Version + "/" + gvr.Resource + " " + namespace + "/" + name), State: Complete, ReadAt: time.Now()}
	if err != nil {
		coverage.State = Unknown
		if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
			coverage.State = "denied"
		} else if NamedAbsent(err, gvr, name) {
			coverage.State = "absent"
		}
	}
	return obj, coverage
}

// Collect uses at most two named reads. No lists,
// discovery, Secrets, CLI execution, arbitrary provider URLs or probes occur.
func Collect(parent context.Context, reader dynamic.Interface, scope *provider.Scope, customJump bool, now time.Time) *Report {
	ctx, cancel := context.WithTimeout(parent, CollectionTimeout)
	defer cancel()
	report := &Report{Scope: boundedScope(scope), CapturedAt: now, CustomJump: customJump,
		Actions: []Action{{Kind: "yaml", Label: "Return to browser: generic YAML"}, {Kind: "describe", Label: "Return to browser: generic describe"}}}
	if customJump {
		report.Actions = append(report.Actions, Action{Kind: "custom jump", Label: "Return to browser: configured custom jump (Enter)"})
	}
	if scope == nil || reader == nil || ctx.Err() != nil {
		report.Coverage = []Coverage{{Source: CertificateGVR, State: Unknown, ReadAt: now}}
		return report
	}
	if !Supports(scope.GVR) {
		report.Coverage = []Coverage{{Source: safe(scope.GVR), State: Unsupported, ReadAt: now}}
		return report
	}
	if !ValidScope(scope) {
		report.Coverage = []Coverage{{Source: CertificateGVR, State: "unknown identity", ReadAt: now}}
		return report
	}
	gvr := schema.GroupVersionResource{Group: Group, Version: Version, Resource: CertificateResource}
	obj, coverage := read(ctx, reader, gvr, scope.TargetNamespace, scope.Name)
	if coverage.State != Complete {
		report.Coverage = append(report.Coverage, coverage)
		return report
	}
	if !validObject(obj, gvr, CertificateKind, scope.TargetNamespace, scope.Name) || string(obj.GetUID()) != scope.UID {
		coverage.State = "unsupported schema or identity"
		report.Coverage = append(report.Coverage, coverage)
		return report
	}
	report.Identity = identity(scope, gvr, obj)
	report.NotBefore = timestamp(obj, "status", "notBefore")
	report.NotAfter = timestamp(obj, "status", "notAfter")
	report.RenewalTime = timestamp(obj, "status", "renewalTime")
	report.Revision = integer(obj, "status", "revision")
	report.SecretReference = str(obj, "spec", "secretName")
	report.checkFacts(obj)
	report.projectConditions(obj)
	if report.SchemaIssues > 0 {
		coverage.State = "partial schema"
	}
	report.Coverage = append(report.Coverage, coverage)
	report.Issuer = resolveIssuer(ctx, reader, scope, obj)
	report.Coverage = append(report.Coverage, report.Issuer.Coverage)
	return report
}
func (r *Report) checkFacts(obj *unstructured.Unstructured) {
	for _, path := range [][]string{{"metadata", "generation"}, {"status", "revision"}} {
		value, found, err := unstructured.NestedFieldNoCopy(obj.Object, path...)
		if err != nil {
			r.SchemaIssues++
			continue
		}
		if found {
			number, ok := value.(int64)
			if !ok || number < 1 {
				r.SchemaIssues++
			}
		}
	}
	for _, path := range [][]string{{"status", "notBefore"}, {"status", "notAfter"}, {"status", "renewalTime"}} {
		value, found, err := unstructured.NestedString(obj.Object, path...)
		if err != nil {
			r.SchemaIssues++
			continue
		}
		if found {
			if _, err := time.Parse(time.RFC3339, value); err != nil || len(value) > MaxFieldBytes {
				r.SchemaIssues++
			}
		}
	}
	value, found, err := unstructured.NestedString(obj.Object, "spec", "secretName")
	if err != nil || (found && len(value) > MaxFieldBytes) {
		r.SchemaIssues++
	}
}
func (r *Report) projectConditions(obj *unstructured.Unstructured) {
	raw, found, err := unstructured.NestedFieldNoCopy(obj.Object, "status", "conditions")
	if err != nil {
		r.SchemaIssues++
		return
	}
	if !found {
		return
	}
	values, ok := raw.([]any)
	if !ok {
		r.SchemaIssues++
		return
	}
	if len(values) > MaxConditions {
		r.SchemaIssues++
	}
	counts := map[string]int{}
	for _, value := range values[:min(len(values), MaxConditions)] {
		fields, ok := value.(map[string]any)
		if !ok {
			r.SchemaIssues++
			continue
		}
		obj := &unstructured.Unstructured{Object: fields}
		kind, found, fieldErr := unstructured.NestedString(obj.Object, "type")
		if fieldErr != nil || !found {
			r.SchemaIssues++
			continue
		}
		if kind != ConditionReady && kind != ConditionIssuing {
			r.UnsupportedConditions++
			continue
		}
		status, found, fieldErr := unstructured.NestedString(obj.Object, "status")
		if fieldErr != nil || !found {
			status = ""
		}
		if status != ConditionTrue && status != ConditionFalse && status != ConditionUnknown {
			status = ConditionUnknown
			r.SchemaIssues++
		}
		condition := Condition{Type: kind, Status: status, ObservedGeneration: integer(obj, "observedGeneration"), TransitionTime: timestamp(obj, "lastTransitionTime")}
		condition.Meaning = conditionMeaning(&condition, r.Identity.Generation)
		r.Conditions = append(r.Conditions, condition)
		counts[kind]++
	}
	for i := range r.Conditions {
		if counts[r.Conditions[i].Type] > 1 {
			r.Conditions[i].Meaning = "unknown duplicate condition"
			r.SchemaIssues++
		}
	}
}
func conditionMeaning(c *Condition, generation *int64) string {
	if generation == nil || c.ObservedGeneration == nil {
		return "unknown generation"
	}
	if *c.ObservedGeneration < *generation {
		return Stale
	}
	if *c.ObservedGeneration > *generation {
		return "unknown future generation"
	}
	if c.Status == ConditionUnknown {
		return Unknown
	}
	if c.Type == ConditionReady {
		if c.Status == ConditionTrue {
			return "controller reports ready"
		}
		return "controller reports not ready"
	}
	if c.Status == ConditionTrue {
		return "controller reports issuance required"
	}
	return "controller reports not issuing"
}
func resolveIssuer(ctx context.Context, reader dynamic.Interface, scope *provider.Scope, obj *unstructured.Unstructured) Relation {
	group, _, groupErr := unstructured.NestedString(obj.Object, "spec", "issuerRef", "group")
	kind, _, kindErr := unstructured.NestedString(obj.Object, "spec", "issuerRef", "kind")
	name, _, nameErr := unstructured.NestedString(obj.Object, "spec", "issuerRef", "name")
	if groupErr != nil || kindErr != nil || nameErr != nil || len(name) > 253 ||
		len(group) > MaxFieldBytes || len(kind) > MaxFieldBytes || len(validation.IsDNS1123Subdomain(name)) > 0 ||
		(group != "" && len(validation.IsDNS1123Subdomain(group)) > 0) || (kind != "" && len(validation.IsCIdentifier(kind)) > 0) {
		return Relation{Coverage: Coverage{Source: "spec.issuerRef", State: "unsupported schema", ReadAt: time.Now()}}
	}
	ref := Reference{Group: group, Kind: kind, Name: name}
	if ref.Group == "" {
		ref.Group = Group
		ref.Defaulted = true
	}
	if ref.Kind == "" {
		ref.Kind = IssuerKind
		ref.Defaulted = true
	}
	relation := Relation{Declared: ref, Coverage: Coverage{Source: "spec.issuerRef", State: Unsupported, ReadAt: time.Now()}}
	if ref.Group != Group || (ref.Kind != IssuerKind && ref.Kind != ClusterIssuerKind) || len(validation.IsDNS1123Subdomain(ref.Name)) > 0 {
		return relation
	}
	resource := "clusterissuers"
	namespace := ""
	if ref.Kind == IssuerKind {
		resource = "issuers"
		namespace = scope.TargetNamespace
	}
	relation.Declared.Namespace = namespace
	gvr := schema.GroupVersionResource{Group: Group, Version: Version, Resource: resource}
	target, coverage := read(ctx, reader, gvr, namespace, ref.Name)
	relation.Coverage = coverage
	if coverage.State != Complete {
		return relation
	}
	if !validObject(target, gvr, ref.Kind, namespace, ref.Name) {
		relation.Coverage.State = "unsupported schema or identity"
		return relation
	}
	observed := identity(scope, gvr, target)
	relation.Observed = &observed
	return relation
}
func (r *Report) CollectionFailed() bool {
	return len(r.Coverage) == 0 || !strings.HasPrefix(r.Coverage[0].State, Complete)
}
func (r *Report) State() string {
	if len(r.Coverage) == 0 {
		return Unknown
	}
	if r.CollectionFailed() {
		return r.Coverage[0].State
	}
	for _, condition := range r.Conditions {
		if condition.Meaning == Stale {
			return Stale
		}
	}
	if r.SchemaIssues > 0 {
		return "partial schema"
	}
	if len(r.Conditions) == 0 {
		return Unknown
	}
	for _, condition := range r.Conditions {
		if strings.HasPrefix(condition.Meaning, Unknown) {
			return Unknown
		}
	}
	for _, coverage := range r.Coverage {
		if coverage.State != Complete {
			return "partial relation"
		}
	}
	return ControllerReport
}
func showTime(value *time.Time) string {
	if value == nil {
		return Unreported
	}
	return value.UTC().Format(time.RFC3339)
}
func showNumber(value *int64) string {
	if value == nil {
		return Unreported
	}
	return fmt.Sprint(*value)
}
func (r *Report) Render(tab int) string {
	var out strings.Builder
	switch tab {
	case 0:
		fmt.Fprintf(&out, "Captured generation %s | revision %s\n", showNumber(r.Identity.Generation), showNumber(r.Revision))
		for _, c := range r.Conditions {
			fmt.Fprintf(&out, "%s=%s: %s\n", c.Type, c.Status, c.Meaning)
		}
		if len(r.Conditions) == 0 {
			fmt.Fprintln(&out, "Ready/Issuing conditions: Unreported; no health inferred.")
		}
		fmt.Fprintf(&out, "Reported notBefore %s\nReported notAfter %s\nReported renewalTime %s\n", showTime(r.NotBefore), showTime(r.NotAfter), showTime(r.RenewalTime))
		fmt.Fprintln(&out, "Ready is a controller report, not independent live TLS, private-key match or recoverability evidence.")
	case 1:
		for _, c := range r.Conditions {
			fmt.Fprintf(&out, "%s=%s | %s\nobservedGeneration %s | transition %s\n\n", c.Type, c.Status, c.Meaning, showNumber(c.ObservedGeneration), showTime(c.TransitionTime))
		}
		fmt.Fprintf(&out, "Unknown condition types retained as count only: %d\nSchema issues: %d\n", r.UnsupportedConditions, r.SchemaIssues)
	case 2:
		ref := r.Issuer.Declared
		fmt.Fprintf(&out, "Declared issuerRef: %s / %s / %s / %s\n%s\n", ref.Group, ref.Kind, ref.Namespace, ref.Name, r.Issuer.Coverage.State)
		if r.Issuer.Observed != nil {
			id := r.Issuer.Observed
			fmt.Fprintf(&out, "Named target observed UID %s | RV %s\n", showReference(id.UID), showReference(id.ResourceVersion))
		}
		fmt.Fprintln(&out, "Name reference only; source does not report target UID. Ownership unverified.")
		fmt.Fprintf(&out, "Declared Secret reference: %s (not read; not TLS proof)\n", showReference(r.SecretReference))
		for _, action := range r.Actions {
			fmt.Fprintln(&out, action.Label)
		}
	case 3:
		fmt.Fprintf(&out, "Captured %s\nContext %s\n%s / %s / %s\nCaptured selected UID %s\nObserved UID %s | RV %s\n",
			r.CapturedAt.UTC().Format(time.RFC3339), safe(r.Scope.Context), safe(r.Scope.GVR), safe(r.Scope.TargetNamespace),
			safe(r.Scope.Name), showReference(r.Scope.UID), showReference(r.Identity.UID), showReference(r.Identity.ResourceVersion))
		for _, coverage := range r.Coverage {
			fmt.Fprintf(&out, "%s: %s | %s\n", coverage.Source, coverage.State, coverage.ReadAt.UTC().Format(time.RFC3339))
		}
		fmt.Fprintf(&out, "At most %d named reads; %d conditions; %d other facts. No Secret or URL reads.\n", MaxReads, MaxConditions, MaxFacts)
	}
	return out.String()
}

func showReference(value string) string {
	if value == "" {
		return Unreported
	}
	return safe(value)
}
