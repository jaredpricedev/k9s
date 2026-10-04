// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package access

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

const fixtureSubresource = "log"
const fixtureRole = "reader"
const fixtureRoleUID = "role-uid"
const fixtureBindingUID = "binding-uid"
const fixtureHiddenRole = "hidden"
const fixtureSelectedUID = "selected-uid"

const fixtureContext = "lab"
const fixtureNamespace = "apps"
const fixtureUser = "alice"
const fixtureGroup = "team"
const fixtureGVR = "v1/pods"
const fixtureName = "api"

func question() *Question {
	return &Question{Context: fixtureContext, GVR: fixtureGVR, Namespace: fixtureNamespace, Name: fixtureName, Verb: client.GetVerb, User: fixtureUser, Groups: []string{fixtureGroup}, TargetUID: fixtureSelectedUID}
}
func reviewClient(status authorizationv1.SubjectAccessReviewStatus) *fake.Clientset {
	c := fake.NewSimpleClientset()
	c.PrependReactor("create", "subjectaccessreviews", func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, &authorizationv1.SubjectAccessReview{Status: status}, nil
	})
	c.PrependReactor("create", "selfsubjectaccessreviews", func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, &authorizationv1.SelfSubjectAccessReview{Status: status}, nil
	})
	c.PrependReactor("create", "selfsubjectreviews", func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, &authenticationv1.SelfSubjectReview{Status: authenticationv1.SelfSubjectReviewStatus{UserInfo: authenticationv1.UserInfo{Username: fixtureUser, Groups: []string{fixtureGroup}}}}, nil
	})
	return c
}
func TestExactNamespacedClusterAndSubresourceQuestions(t *testing.T) {
	for _, namespace := range []string{fixtureNamespace, ""} {
		t.Run("namespace="+namespace, func(t *testing.T) {
			q := question()
			q.Namespace = namespace
			q.Subresource = fixtureSubresource
			c := reviewClient(authorizationv1.SubjectAccessReviewStatus{Allowed: true})
			s := Collect(t.Context(), c, q)
			if s.Decision != Allowed || s.Review.State != Complete || s.Review.At.IsZero() {
				t.Fatal(s)
			}
			review := c.Actions()[0].(ktesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
			a := review.Spec.ResourceAttributes
			if a.Namespace != namespace || a.Subresource != fixtureSubresource || a.Name != fixtureName || a.Resource != "pods" || a.Group != "" || a.Version != "v1" || review.Spec.User != fixtureUser || len(review.Spec.Groups) != 1 {
				t.Fatal("question broadened", review.Spec)
			}
			for _, action := range c.Actions() {
				if namespace == "" && action.GetResource().Resource == "rolebindings" {
					t.Fatal("cluster question read namespace bindings")
				}
				if action.GetResource().Resource == "pods" || action.GetResource().Resource == "secrets" {
					t.Fatal("queried resource body")
				}
			}
		})
	}
	q := question()
	q.GVR = "apps/v1/deployments"
	q.Subresource = "scale"
	attrs := q.Attributes()
	if attrs.Group != "apps" || attrs.Resource != "deployments" || attrs.Subresource != "scale" {
		t.Fatal(attrs)
	}
}
func TestSelfUsesSSARAndOnlyCurrentSubjectReview(t *testing.T) {
	q := question()
	q.User = Self
	q.Groups = nil
	c := reviewClient(authorizationv1.SubjectAccessReviewStatus{Allowed: true})
	s := Collect(t.Context(), c, q)
	if s.Decision != Allowed || s.SubjectUser != fixtureUser {
		t.Fatal(s)
	}
	if c.Actions()[0].GetResource().Resource != "selfsubjectaccessreviews" || c.Actions()[1].GetResource().Resource != "selfsubjectreviews" {
		t.Fatal(c.Actions())
	}
	for _, action := range c.Actions() {
		if action.GetResource().Resource == "users" || action.GetResource().Resource == "groups" {
			t.Fatal("identity enumeration")
		}
	}
}
func TestReviewSubmissionDeniedIsNotQuestionDenied(t *testing.T) {
	c := reviewClient(authorizationv1.SubjectAccessReviewStatus{})
	c.PrependReactor("create", "subjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: authorizationv1.GroupName, Resource: "subjectaccessreviews"}, "", errors.New("operator cannot submit"))
	})
	s := Collect(t.Context(), c, question())
	if s.Review.State != Denied || s.Decision != Unavailable || len(c.Actions()) != 1 || s.Provenance != Incomplete {
		t.Fatal(s, c.Actions())
	}
	c = reviewClient(authorizationv1.SubjectAccessReviewStatus{Denied: true, Reason: "Policy declined"})
	s = Collect(t.Context(), c, question())
	if s.Decision != Denied || s.Review.State != Complete {
		t.Fatal(s)
	}
	c = reviewClient(authorizationv1.SubjectAccessReviewStatus{EvaluationError: "opaque authorizer unavailable"})
	s = Collect(t.Context(), c, question())
	if s.Decision != Unavailable || !strings.Contains(s.Text(), "opaque authorizer unavailable") {
		t.Fatal(s)
	}
}
func TestBindingAndRoleVisibilityDoNotInferNoGrants(t *testing.T) {
	for _, resource := range []string{"rolebindings", "clusterrolebindings"} {
		c := reviewClient(authorizationv1.SubjectAccessReviewStatus{Allowed: true})
		c.PrependReactor("list", resource, func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: rbacv1.GroupName, Resource: resource}, "", nil)
		})
		s := Collect(t.Context(), c, question())
		if s.Decision != Allowed || s.Provenance != Incomplete || !strings.Contains(s.Text(), "does not establish absence of grants") {
			t.Fatal(s)
		}
	}
	c := reviewClient(authorizationv1.SubjectAccessReviewStatus{Allowed: true})
	binding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "visible", Namespace: fixtureNamespace, UID: fixtureBindingUID}, Subjects: []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: fixtureUser, APIGroup: rbacv1.GroupName}}, RoleRef: rbacv1.RoleRef{Kind: roleKindNamespaced, Name: fixtureHiddenRole, APIGroup: rbacv1.GroupName}}
	if err := c.Tracker().Add(binding); err != nil {
		t.Fatal(err)
	}
	s := Collect(t.Context(), c, question())
	if len(s.Candidates) != 1 || s.Provenance != Incomplete || s.Candidates[0].Binding.UID != fixtureBindingUID || s.Candidates[0].Rules != 0 {
		t.Fatal(s)
	}
}
func TestVisibleRuleCandidatesRespectBindingAndResourceScope(t *testing.T) {
	c := reviewClient(authorizationv1.SubjectAccessReviewStatus{Allowed: true})
	objects := []runtime.Object{
		&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: fixtureRole, Namespace: fixtureNamespace, UID: fixtureRoleUID}, Rules: []rbacv1.PolicyRule{{Verbs: []string{client.GetVerb}, APIGroups: []string{""}, Resources: []string{"pods/log"}, ResourceNames: []string{fixtureName}}}},
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "reader-binding", Namespace: fixtureNamespace, UID: fixtureBindingUID}, RoleRef: rbacv1.RoleRef{Kind: roleKindNamespaced, Name: fixtureRole, APIGroup: rbacv1.GroupName}, Subjects: []rbacv1.Subject{{Kind: rbacv1.GroupKind, Name: fixtureGroup, APIGroup: rbacv1.GroupName}}},
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "another"}, RoleRef: rbacv1.RoleRef{Kind: roleKindNamespaced, Name: fixtureRole, APIGroup: rbacv1.GroupName}, Subjects: []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: fixtureUser, APIGroup: rbacv1.GroupName}}},
	}
	for _, obj := range objects {
		if err := c.Tracker().Add(obj); err != nil {
			t.Fatal(err)
		}
	}
	q := question()
	q.Subresource = fixtureSubresource
	s := Collect(t.Context(), c, q)
	if len(s.Candidates) != 1 || s.Candidates[0].Rules != 1 || s.Candidates[0].Role.UID != fixtureRoleUID || s.Provenance != Complete {
		t.Fatal(s)
	}
	q.Subresource = ""
	s = Collect(t.Context(), c, q)
	if s.Candidates[0].Rules != 0 {
		t.Fatal("subresource rule granted parent resource")
	}
	if !subjectMatches("system:serviceaccount:"+fixtureNamespace+":worker", nil, []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: "worker"}}, fixtureNamespace) {
		t.Fatal("RoleBinding SA namespace default lost")
	}
	if subjectMatches("system:serviceaccount:"+fixtureNamespace+":worker", nil, []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: "worker"}}, "") {
		t.Fatal("ClusterRoleBinding inferred SA namespace")
	}
}
func TestBoundedBindingsAndRoleReadsRemainIncomplete(t *testing.T) {
	c := reviewClient(authorizationv1.SubjectAccessReviewStatus{Allowed: true})
	c.PrependReactor("list", "clusterrolebindings", func(ktesting.Action) (bool, runtime.Object, error) {
		out := &rbacv1.ClusterRoleBindingList{ListMeta: metav1.ListMeta{Continue: "next-page"}}
		for i := range MaxBindings + 1 {
			out.Items = append(out.Items, rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("binding-%d", i)}, Subjects: []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: fixtureUser, APIGroup: rbacv1.GroupName}}, RoleRef: rbacv1.RoleRef{Kind: roleKindCluster, Name: fixtureHiddenRole, APIGroup: rbacv1.GroupName}})
		}
		return true, out, nil
	})
	s := Collect(t.Context(), c, question())
	if len(s.Candidates) != MaxRoles || s.Provenance != Incomplete {
		t.Fatal(s)
	}
	gets := 0
	for _, action := range c.Actions() {
		if action.GetVerb() == client.GetVerb {
			gets++
		}
	}
	if gets != MaxRoles {
		t.Fatal("role read bound escaped", gets)
	}
}
func TestCanceledReviewAndInvalidQuestionPerformNoReads(t *testing.T) {
	q := question()
	q.Verb = "*"
	c := reviewClient(authorizationv1.SubjectAccessReviewStatus{})
	s := Collect(t.Context(), c, q)
	if len(c.Actions()) != 0 || s.Decision != Unavailable {
		t.Fatal(s)
	}
	q = question()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	c.PrependReactor("create", "subjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, context.Canceled })
	s = Collect(ctx, c, q)
	if s.Decision != Unavailable || s.Review.State != Unavailable {
		t.Fatal(s)
	}
	if time.Since(s.At) > time.Second {
		t.Fatal("canceled review exceeded bound")
	}
}

func TestNamedCreateSubresourceKeepsParentIdentity(t *testing.T) {
	q := question()
	q.Verb = "create"
	q.Subresource = "exec"
	if err := q.Validate(); err != nil {
		t.Fatal(err)
	}
	if q.Attributes().Name != fixtureName {
		t.Fatal("named subresource lost parent")
	}
	q.Subresource = ""
	if q.Validate() == nil {
		t.Fatal("named parent-resource create falsely represented real authorization")
	}
}
func TestCurrentSubjectDeniedKeepsAllowedDecisionAndIncompleteProvenance(t *testing.T) {
	q := question()
	q.User = Self
	q.Groups = nil
	c := reviewClient(authorizationv1.SubjectAccessReviewStatus{Allowed: true})
	c.PrependReactor("create", "selfsubjectreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: authenticationv1.GroupName, Resource: "selfsubjectreviews"}, "", nil)
	})
	s := Collect(t.Context(), c, q)
	if s.Decision != Allowed || s.Subject.State != Denied || s.Provenance != Incomplete || len(c.Actions()) != 2 {
		t.Fatal(s, c.Actions())
	}
}
