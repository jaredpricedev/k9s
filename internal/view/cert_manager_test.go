// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package view

import (
	"errors"
	"strings"
	"testing"

	"github.com/derailed/k9s/internal/certmanager"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/model1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
)

func TestCertificateReferenceResolutionUsesDiscoveredScope(t *testing.T) {
	m := dao.NewMeta()
	m.RegisterMeta("private.pki/v1beta1/issuers", &metav1.APIResource{Kind: "Issuer", Namespaced: true})
	m.RegisterMeta("private.pki/v1/issuers", &metav1.APIResource{Kind: "Issuer", Namespaced: true})
	m.RegisterMeta("cert-manager.io/v1/clusterissuers", &metav1.APIResource{Kind: "ClusterIssuer"})
	for _, tt := range []struct {
		ref        certmanager.Reference
		gvr        string
		namespaced bool
	}{
		{certmanager.Reference{Group: "private.pki", Kind: "Issuer", Namespace: "apps", Name: "vault"}, "private.pki/v1/issuers", true},
		{certmanager.Reference{Group: "cert-manager.io", Kind: "ClusterIssuer", Name: "ca"}, "cert-manager.io/v1/clusterissuers", false},
	} {
		gvr, ns, err := resolveCertificateReference(m, tt.ref)
		require.NoError(t, err)
		assert.Equal(t, tt.gvr, gvr.String())
		assert.Equal(t, tt.namespaced, ns)
	}
	_, _, err := resolveCertificateReference(m, certmanager.Reference{Group: "other.io", Kind: "Issuer"})
	require.Error(t, err)
}

func TestCertificateChildrenMatchIdentityAndNamespace(t *testing.T) {
	parent := &unstructured.Unstructured{}
	parent.SetAPIVersion("cert-manager.io/v1")
	parent.SetKind("Certificate")
	parent.SetNamespace("apps")
	parent.SetName("tls")
	parent.SetUID("current-uid")
	child := func(name, ns string, uid types.UID, group string) runtime.Object {
		o := &unstructured.Unstructured{}
		o.SetAPIVersion("cert-manager.io/v1")
		o.SetKind("CertificateRequest")
		o.SetNamespace(ns)
		o.SetName(name)
		o.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: group + "/v1", Kind: "Certificate", Name: "tls", UID: uid}})
		return o
	}
	refs := certificateChildren(parent, []runtime.Object{
		child("tls-2", "apps", "current-uid", "cert-manager.io"),
		child("tls-1", "apps", "old-uid", "cert-manager.io"),
		child("tls-3", "other", "current-uid", "cert-manager.io"),
		child("tls-4", "apps", "current-uid", "other.io"),
	})
	require.Len(t, refs, 1)
	assert.Equal(t, "tls-2", refs[0].Name)
	parent.SetUID("")
	assert.Empty(t, certificateChildren(parent, []runtime.Object{child("tls-2", "apps", "", "cert-manager.io")}))
}

func TestCertManagerViewerUsesReadOnlyNavigation(t *testing.T) {
	v := NewCertManager(client.NewGVR("cert-manager.io/v1/certificates"))
	assert.Equal(t, "certificates", v.GVR().R())
	assert.NotNil(t, v.GetTable())
}

type certificateRelationFactory struct {
	dao.Factory
	listErr     error
	synced      bool
	requestedNS string
}

func (f *certificateRelationFactory) List(_ *client.GVR, ns string, _ bool, _ labels.Selector) ([]runtime.Object, error) {
	f.requestedNS = ns
	return nil, f.listErr
}

func (f *certificateRelationFactory) CanForResource(string, *client.GVR, []string) (informers.GenericInformer, error) {
	return certificateGenericInformer{synced: f.synced}, nil
}

type certificateGenericInformer struct {
	informers.GenericInformer
	synced bool
}

func (i certificateGenericInformer) Informer() cache.SharedIndexInformer {
	return certificateSharedInformer{synced: i.synced}
}

type certificateSharedInformer struct {
	cache.SharedIndexInformer
	synced bool
}

func (i certificateSharedInformer) HasSynced() bool { return i.synced }

func TestCertificateRelationshipsDistinguishLoadingDeniedAndEmpty(t *testing.T) {
	m := dao.NewMeta()
	m.RegisterMeta("cert-manager.io/v1/certificaterequests", &metav1.APIResource{Kind: "CertificateRequest", Namespaced: true})
	o := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "cert-manager.io/v1", "kind": "Certificate", "metadata": map[string]any{"name": "tls", "namespace": "apps", "uid": "current"}, "spec": map[string]any{"secretName": "tls"}}}
	for _, tt := range []struct {
		name   string
		synced bool
		err    error
		want   string
	}{
		{name: "loading", want: "Loading CertificateRequest cache"},
		{name: "empty", synced: true, want: "No owned CertificateRequest resources found"},
		{name: "denied", err: errors.New("access denied"), want: "Cannot list CertificateRequest: access denied"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &certificateRelationFactory{synced: tt.synced, listErr: tt.err}
			items := certificateRelatedItems(f, m, o)
			require.Len(t, items, 2)
			assert.Equal(t, "Secret", items[0].ref.Kind)
			assert.Contains(t, items[1].notice, tt.want)
			assert.Equal(t, "apps", f.requestedNS)
		})
	}
	items := certificateRelatedItems(&certificateRelationFactory{}, dao.NewMeta(), o)
	require.Len(t, items, 2)
	assert.Contains(t, items[1].notice, "not available in this context")
}

func TestCertificateStatusDetailsPreserveLongMessagesAndEscapeMarkup(t *testing.T) {
	message := "[red]" + strings.Repeat("renewal failed: ", 60)
	header := model1.Header{{Name: "NAME"}, {Name: "MESSAGE"}, {Name: "CUSTOM"}}
	row := &model1.Row{Fields: model1.Fields{"tls", message, "do not include arbitrary custom fields"}}
	text := certificateStatusDetails(header, row)
	assert.Contains(t, text, "[red[]")
	assert.Contains(t, text, strings.Repeat("renewal failed: ", 60))
	assert.NotContains(t, text, "arbitrary")
	assert.Empty(t, certificateStatusDetails(header, nil))
	assert.NotPanics(t, func() { certificateStatusDetails(header, &model1.Row{}) })
}
