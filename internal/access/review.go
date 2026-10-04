// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

// Package access answers one explicit authorization question. Authorization
// review results remain separate from incomplete, visible RBAC provenance.
package access

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/logstream"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
)

const MaxBindings = 100
const MaxRoles = 32
const Timeout = 8 * time.Second
const Self = "self"
const roleKindCluster = "ClusterRole"
const roleKindNamespaced = "Role"
const Allowed = "allowed"
const Denied = "denied"
const Unavailable = "unavailable"
const Incomplete = "incomplete"
const Complete = "complete"

// Question represents resource authorization only, never a resource operation.
// An empty Namespace is explicitly cluster scope, not an all-namespace probe.
type Question struct {
	Context, GVR, Namespace, Name, Subresource, Verb, User, TargetUID string
	Groups                                                            []string
}
type Request struct {
	Source, State, Detail string
	At                    time.Time
}
type Candidate struct {
	Binding, Role inspect.ResourceIdentity
	Rules         int
	Detail        string
}
type Snapshot struct {
	Question                          Question
	Decision, Reason, EvaluationError string
	Review                            Request
	Subject                           Request
	SubjectUser                       string
	SubjectUID                        string
	SubjectGroups                     []string
	Provenance                        string
	Reads                             []Request
	Candidates                        []Candidate
	At                                time.Time
}

var gvrPattern = regexp.MustCompile(`^(?:[a-z0-9][a-z0-9.-]*/)?v[0-9]+(?:alpha[0-9]+|beta[0-9]+)?/[a-z][a-z0-9]*$`)
var tokenPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Validate never resolves resource scope or identities through discovery.
func (q *Question) Validate() error {
	if q.Context == "" || !gvrPattern.MatchString(q.GVR) || len(q.GVR) > 320 || !tokenPattern.MatchString(q.Verb) || q.User == "" {
		return errors.New("provide context, explicit GVR, concrete verb and self or an explicit username")
	}
	parts := strings.Split(q.GVR, "/")
	if (len(parts) == 3 && len(validation.IsDNS1123Subdomain(parts[0])) != 0) || len(parts[len(parts)-1]) > 63 || len(parts[len(parts)-2]) > 63 {
		return errors.New("invalid GVR group/version/resource")
	}
	if q.Namespace != "" && len(validation.IsDNS1123Label(q.Namespace)) != 0 {
		return errors.New("namespace must be explicit, or select cluster scope")
	}
	if q.Name != "" && ((q.Verb == "create" && q.Subresource == "") || q.Verb == "deletecollection") {
		return errors.New("parent create/deletecollection authorize a collection; clear the resource name")
	}
	if q.Name != "" && len(validation.IsDNS1123Subdomain(q.Name)) != 0 {
		return errors.New("invalid resource name")
	}
	if q.Subresource != "" && !tokenPattern.MatchString(q.Subresource) {
		return errors.New("invalid subresource")
	}
	if len(q.Groups) > 32 {
		return errors.New("at most 32 explicit groups")
	}
	for _, value := range append([]string{q.Context, q.GVR, q.User, q.TargetUID}, q.Groups...) {
		if len(value) > 512 || logstream.SafeText(value) != value {
			return errors.New("identity fields must be bounded plain text")
		}
	}
	if q.User == Self && len(q.Groups) > 0 {
		return errors.New("self groups come from the authenticated identity, not form overrides")
	}
	return nil
}
func (q *Question) Attributes() *authorizationv1.ResourceAttributes {
	parts := strings.Split(q.GVR, "/")
	group := ""
	if len(parts) == 3 {
		group = parts[0]
	}
	return &authorizationv1.ResourceAttributes{Namespace: q.Namespace, Verb: q.Verb, Group: group,
		Version: parts[len(parts)-2], Resource: parts[len(parts)-1], Subresource: q.Subresource, Name: q.Name}
}

// Collect submits exactly one SSAR/SAR. Creating these nonpersisted review
// objects evaluates policy; it never creates or changes the questioned resource.
func Collect(ctx context.Context, client kubernetes.Interface, q *Question) *Snapshot {
	captured := *q
	captured.Groups = slices.Clone(q.Groups)
	s := &Snapshot{Question: captured, Decision: Unavailable, Provenance: Incomplete, At: time.Now().UTC(),
		Subject: Request{Source: "Subject provenance not collected", State: Unavailable}}
	if err := captured.Validate(); err != nil {
		s.Review = Request{Source: "Question validation", State: Unavailable, Detail: err.Error(), At: s.At}
		return s
	}
	if ctx.Err() != nil {
		s.Review = readResult("Explicit authorization review (canceled before request)", ctx.Err())
		return s
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	reviewSource := "authorization.k8s.io/v1 subjectaccessreviews create"
	var status authorizationv1.SubjectAccessReviewStatus
	var err error
	if captured.User == Self {
		reviewSource = "authorization.k8s.io/v1 selfsubjectaccessreviews create"
		request := &authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: captured.Attributes()}}
		response, readErr := client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, request, metav1.CreateOptions{})
		err = readErr
		if response != nil {
			status = response.Status
		}
	} else {
		request := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
			User: captured.User, Groups: captured.Groups, ResourceAttributes: captured.Attributes()}}
		response, readErr := client.AuthorizationV1().SubjectAccessReviews().Create(ctx, request, metav1.CreateOptions{})
		err = readErr
		if response != nil {
			status = response.Status
		}
	}
	s.Review = readResult(reviewSource, err)
	if err != nil {
		return s
	}
	s.Reason, s.EvaluationError = boundedText(status.Reason), boundedText(status.EvaluationError)
	switch {
	case status.Allowed && status.Denied:
		s.Decision = Unavailable
		s.EvaluationError = "Inconsistent authorization review status"
	case status.Allowed:
		s.Decision = Allowed
	case status.Denied:
		s.Decision = Denied
	case status.EvaluationError != "":
		s.Decision = Unavailable
	default:
		s.Decision = Denied
	}
	if captured.User == Self {
		response, readErr := client.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
		s.Subject = readResult("authentication.k8s.io/v1 selfsubjectreviews create (current identity only)", readErr)
		if readErr != nil || response == nil || response.Status.UserInfo.Username == "" {
			if readErr == nil {
				s.Subject.State = Unavailable
				s.Subject.Detail = "Current authenticated identity was not reported"
			}
			return s
		}
		s.SubjectUser = response.Status.UserInfo.Username
		s.SubjectUID = response.Status.UserInfo.UID
		s.SubjectGroups = response.Status.UserInfo.Groups
	} else {
		s.SubjectUser = captured.User
		s.SubjectGroups = slices.Clone(captured.Groups)
		s.Subject = Request{Source: "Explicit supplied subject and groups; membership not inferred", State: Complete, At: time.Now().UTC()}
	}
	s.collectBindings(ctx, client)
	return s
}

func readResult(source string, err error) Request {
	r := Request{Source: source, State: Complete, At: time.Now().UTC()}
	if err != nil {
		r.State = Unavailable
		r.Detail = "Request failed, unsupported or timed out"
		if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
			r.State = Denied
			r.Detail = "Operator lacks permission to submit this request/read; questioned decision is separate"
		}
	}
	return r
}

func (s *Snapshot) collectBindings(ctx context.Context, client kubernetes.Interface) {
	q := &s.Question
	s.Provenance = Complete
	cb, err := client.RbacV1().ClusterRoleBindings().List(ctx, metav1.ListOptions{Limit: MaxBindings + 1})
	s.Reads = append(s.Reads, readResult("rbac.authorization.k8s.io/v1 clusterrolebindings list", err))
	if err != nil {
		s.Provenance = Incomplete
	} else if cb != nil {
		if cb.Continue != "" || len(cb.Items) > MaxBindings {
			s.Provenance = Incomplete
			s.Reads[len(s.Reads)-1].State = Incomplete
			s.Reads[len(s.Reads)-1].Detail = "Bounded visible page; further bindings may exist"
		}
		for i := range cb.Items[:min(len(cb.Items), MaxBindings)] {
			b := &cb.Items[i]
			s.addBinding(ctx, client, b.Name, "", string(b.UID), b.Subjects, b.RoleRef)
		}
	}
	if q.Namespace == "" {
		return
	}
	rb, err := client.RbacV1().RoleBindings(q.Namespace).List(ctx, metav1.ListOptions{Limit: MaxBindings + 1})
	s.Reads = append(s.Reads, readResult("rbac.authorization.k8s.io/v1 rolebindings list namespace="+q.Namespace, err))
	if err != nil {
		s.Provenance = Incomplete
	} else if rb != nil {
		if rb.Continue != "" || len(rb.Items) > MaxBindings {
			s.Provenance = Incomplete
			s.Reads[len(s.Reads)-1].State = Incomplete
			s.Reads[len(s.Reads)-1].Detail = "Bounded visible page; further bindings may exist"
		}
		for i := range rb.Items[:min(len(rb.Items), MaxBindings)] {
			b := &rb.Items[i]
			s.addBinding(ctx, client, b.Name, b.Namespace, string(b.UID), b.Subjects, b.RoleRef)
		}
	}
}
func (s *Snapshot) addBinding(ctx context.Context, client kubernetes.Interface, name, namespace, uid string, subjects []rbacv1.Subject, ref rbacv1.RoleRef) {
	if !subjectMatches(s.SubjectUser, s.SubjectGroups, subjects, namespace) {
		return
	}
	if len(s.Candidates) >= MaxRoles {
		s.Provenance = Incomplete
		return
	}
	q := &s.Question
	kind := "clusterrolebindings"
	if namespace != "" {
		kind = "rolebindings"
	}
	c := Candidate{Binding: inspect.ResourceIdentity{Context: q.Context, GVR: "rbac.authorization.k8s.io/v1/" + kind, Namespace: namespace, Name: name, UID: uid},
		Detail: "Visible subject match; referenced rules not yet obtained"}
	var rules []rbacv1.PolicyRule
	var err error
	if ref.APIGroup != rbacv1.GroupName {
		s.Provenance = Incomplete
		c.Detail = "Unsupported role reference API group; no role read requested"
		s.Candidates = append(s.Candidates, c)
		return
	}
	switch ref.Kind {
	case roleKindCluster:
		c.Role = inspect.ResourceIdentity{Context: q.Context, GVR: "rbac.authorization.k8s.io/v1/clusterroles", Name: ref.Name}
		var role *rbacv1.ClusterRole
		role, err = client.RbacV1().ClusterRoles().Get(ctx, ref.Name, metav1.GetOptions{})
		if role != nil {
			rules = role.Rules
			c.Role = inspect.ResourceIdentity{Context: q.Context, GVR: "rbac.authorization.k8s.io/v1/clusterroles", Name: role.Name, UID: string(role.UID)}
		}
	case roleKindNamespaced:
		c.Role = inspect.ResourceIdentity{Context: q.Context, GVR: "rbac.authorization.k8s.io/v1/roles", Namespace: namespace, Name: ref.Name}
		if namespace == "" {
			err = errors.New("invalid cluster binding Role reference")
			break
		}
		var role *rbacv1.Role
		role, err = client.RbacV1().Roles(namespace).Get(ctx, ref.Name, metav1.GetOptions{})
		if role != nil {
			rules = role.Rules
			c.Role = inspect.ResourceIdentity{Context: q.Context, GVR: "rbac.authorization.k8s.io/v1/roles", Namespace: namespace, Name: role.Name, UID: string(role.UID)}
		}
	default:
		err = errors.New("unsupported role reference")
	}
	r := readResult("Referenced "+ref.Kind+" "+namespace+"/"+ref.Name+" get", err)
	s.Reads = append(s.Reads, r)
	if err != nil {
		s.Provenance = Incomplete
		c.Detail = "Matching visible binding; referenced role unavailable"
	} else {
		c.Detail = "Visible subject match; no matching rule in this referenced role"
		if len(rules) > MaxBindings {
			s.Provenance = Incomplete
			s.Reads[len(s.Reads)-1].State = Incomplete
			s.Reads[len(s.Reads)-1].Detail = "Referenced rules truncated at 100; candidate explanation incomplete"
		}
		for index := range rules[:min(len(rules), MaxBindings)] {
			if ruleMatches(&rules[index], q.Attributes()) {
				c.Rules++
				c.Detail = "Visible candidate rule match; does not prove the authorizer's cause"
			}
		}
	}
	s.Candidates = append(s.Candidates, c)
}
func subjectMatches(user string, groups []string, subjects []rbacv1.Subject, bindingNamespace string) bool {
	for _, subject := range subjects {
		switch subject.Kind {
		case rbacv1.UserKind:
			if subject.Name == user {
				return true
			}
		case rbacv1.GroupKind:
			if slices.Contains(groups, subject.Name) {
				return true
			}
		case rbacv1.ServiceAccountKind:
			ns := subject.Namespace
			if ns == "" {
				ns = bindingNamespace
			}
			if ns != "" && user == "system:serviceaccount:"+ns+":"+subject.Name {
				return true
			}
		}
	}
	return false
}
func ruleMatches(rule *rbacv1.PolicyRule, a *authorizationv1.ResourceAttributes) bool {
	matches := func(values []string, value string) bool {
		return slices.Contains(values, value) || slices.Contains(values, "*")
	}
	if !matches(rule.Verbs, a.Verb) || !matches(rule.APIGroups, a.Group) {
		return false
	}
	resource := a.Resource
	if a.Subresource != "" {
		resource += "/" + a.Subresource
	}
	found := matches(rule.Resources, resource)
	if a.Subresource != "" && slices.Contains(rule.Resources, "*/"+a.Subresource) {
		found = true
	}
	if !found {
		return false
	}
	return len(rule.ResourceNames) == 0 || (a.Name != "" && slices.Contains(rule.ResourceNames, a.Name))
}

func (s *Snapshot) Text() string {
	var b strings.Builder
	q := &s.Question
	scope := "cluster (empty namespace)"
	if q.Namespace != "" {
		scope = "namespace " + q.Namespace
	}
	fmt.Fprintf(&b, "ACCESS DECISION: %s\nSubject: %s\nVerb: %s\nResource: %s\nScope: %s\n"+
		"Subresource: %s\nName: %s\nContext: %s\nSelected UID: %s (not part of authorization attributes)\n"+
		"Review time: %s\n",
		s.Decision, q.User, q.Verb, q.GVR, scope, q.Subresource, q.Name, q.Context, q.TargetUID, observed(s.Review.At))
	fmt.Fprintf(&b, "\nREVIEW REQUEST: %s\nSource: %s\n%s\nDecision reason: %s\nEvaluation error: %s\n",
		s.Review.State, s.Review.Source, s.Review.Detail, s.Reason, s.EvaluationError)
	fmt.Fprintf(&b, "\nSUBJECT: %s\n%s\n%s\nResolved/supplied user: %s\nCurrent subject UID: %s\nGroups: %s\nSubject observed: %s\n",
		s.Subject.State, s.Subject.Source, s.Subject.Detail, s.SubjectUser, s.SubjectUID, strings.Join(s.SubjectGroups, ", "), observed(s.Subject.At))
	fmt.Fprintf(&b, "\nVISIBLE RBAC EXPLANATION: %s\n", s.Provenance)
	for index := range s.Candidates {
		c := &s.Candidates[index]
		fmt.Fprintf(&b, "Binding %s %s/%s UID=%s\nRole %s %s/%s UID=%s\n%d matching rules: %s\n",
			c.Binding.GVR, c.Binding.Namespace, c.Binding.Name, c.Binding.UID,
			c.Role.GVR, c.Role.Namespace, c.Role.Name, c.Role.UID, c.Rules, c.Detail)
	}
	if len(s.Candidates) == 0 {
		b.WriteString("No subject-matching bindings retained in the visible bounded page; this does not establish absence of grants.\n")
	}
	for _, r := range s.Reads {
		fmt.Fprintf(&b, "\n%s: %s at %s\n%s\n", r.Source, r.State, observed(r.At), r.Detail)
	}
	b.WriteString("\nNames/groups are exact inputs; no identity enumeration or inferred membership.\n" +
		"Empty explicit groups exclude group grants and may differ from authenticated membership.\n" +
		"Named list/watch checks require the corresponding metadata.name field selector in a real request.\n" +
		"Resource scope is an explicit operator input, not automatically discovered. RoleBindings apply only within their namespace.\n" +
		"Resource existence, other authorizers, admission and later identity changes are not established by visible rules.\n" +
		"Candidate rules do not prove why an authorization decision occurred.\n" +
		"Only nonpersisted review objects and bounded RBAC reads are requested; no questioned resource or Secret body is read or changed.\n")
	return logstream.SafeText(b.String())
}

func boundedText(text string) string {
	if len(text) > 4096 {
		text = strings.ToValidUTF8(text[:4096], "") + " (truncated at 4 KiB)"
	}
	return logstream.SafeText(text)
}

func observed(at time.Time) string {
	if at.IsZero() {
		return "not reported"
	}
	return at.UTC().Format(time.RFC3339)
}
