package backup

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
)

func TestBoundedProjectionRejectsIdentityAndExcludesSensitiveFields(t *testing.T) {
	sourceTime := time.Date(2026, 10, 4, 12, 34, 56, 0, time.FixedZone("source", 2*60*60))
	kinds := map[schema.GroupVersionResource]string{}
	for _, x := range []string{"schedules", "backups", "restores"} {
		kinds[schema.GroupVersionResource{Group: "velero.io", Version: "v1", Resource: x}] = strings.TrimSuffix(strings.Title(x), "s") + "List"
	} //nolint:staticcheck // Fixed ASCII resource names.
	reader := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), kinds)
	reader.PrependReactor("list", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
		require.Equal(t, "velero", action.GetNamespace())
		list := &unstructured.UnstructuredList{}
		if action.GetResource().Resource == "backups" {
			for i := range 101 {
				list.Items = append(list.Items, unstructured.Unstructured{Object: map[string]any{"apiVersion": "velero.io/v1", "kind": "Backup", "metadata": map[string]any{"name": fmt.Sprint(i), "namespace": "velero", "uid": fmt.Sprint(i), "annotations": map[string]any{"credential": "sentinel-secret"}}, "status": map[string]any{"phase": "Completed", "message": "sentinel-secret", "errors": int64(0)}}})
			}
			metadata := list.Items[4].Object["metadata"].(map[string]any)
			metadata["creationTimestamp"] = sourceTime.Format(time.RFC3339)
			list.Items[0].SetKind("Secret")
			list.Items[1].SetUID("")
			list.Items[2].SetNamespace("wrong")
		}
		return true, list, nil
	})
	s := Collect(t.Context(), reader, &Scope{Context: "chosen", Namespace: "apps", ControllerNamespace: "velero"}, time.Now())
	require.Len(t, s.Records, 97)
	require.Nil(t, s.Records[0].CreatedAt)
	require.Contains(t, s.Render(2), "Created Unreported")
	require.NotContains(t, s.Render(2), "0001-01-01")
	require.Equal(t, sourceTime.UTC(), *s.Records[1].CreatedAt)
	require.Contains(t, s.Render(2), "Created "+sourceTime.UTC().Format(time.RFC3339))
	require.True(t, s.Partial())
	require.NotContains(t, fmt.Sprintf("%+v", s), "sentinel-secret")
	require.Contains(t, s.Render(0), "Application restore-test evidence: Unreported")
	require.Contains(t, s.Render(4), "partial / identity rejected")
	require.Len(t, reader.Actions(), 3)
}
func TestStructuredNamedAbsenceAndProxy404(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "velero.io", Version: "v1", Resource: "backups"}
	require.True(t, NamedAbsent(apierrors.NewNotFound(gvr.GroupResource(), "named"), gvr, "named"))
	require.False(t, NamedAbsent(apierrors.NewNotFound(gvr.GroupResource(), "other"), gvr, "named"))
	require.False(t, NamedAbsent(&apierrors.StatusError{ErrStatus: metav1.Status{Code: 404, Reason: metav1.StatusReasonNotFound}}, gvr, "named"))
}
func TestHTTPReadsCancellationAndProxy404RemainUnknown(t *testing.T) {
	for _, cancelRead := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelRead), func(t *testing.T) {
			started := make(chan struct{}, 3)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "GET", r.Method)
				assert.Contains(t, r.URL.Path, "/namespaces/velero/")
				assert.Equal(t, "101", r.URL.Query().Get("limit"))
				started <- struct{}{}
				if cancelRead {
					<-r.Context().Done()
					return
				}
				w.WriteHeader(http.StatusNotFound)
				_, err := w.Write([]byte("proxy missing sentinel-secret"))
				assert.NoError(t, err)
			}))
			defer server.Close()
			reader, err := dynamic.NewForConfig(&rest.Config{Host: server.URL})
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan *Snapshot, 1)
			go func() {
				done <- Collect(ctx, reader, &Scope{Context: "chosen", Namespace: "apps", ControllerNamespace: "velero"}, time.Now())
			}()
			if cancelRead {
				<-started
				cancel()
			}
			select {
			case s := <-done:
				require.Empty(t, s.Records)
				require.Len(t, s.Coverage, 3)
				for _, c := range s.Coverage {
					require.Equal(t, "unknown / unavailable", c.State)
				}
				require.NotContains(t, fmt.Sprint(s), "sentinel-secret")
			case <-time.After(time.Second):
				t.Fatal("collection cancellation blocked")
			}
		})
	}
}

func TestProjectionCapsAndUnavailableAreNotEmpty(t *testing.T) {
	values := make([]any, 10000)
	for i := range values {
		values[i] = strings.Repeat("x", MaxFieldBytes+1)
	}
	obj := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"includedNamespaces": values}}}
	projected, limited := declaredProjection(obj, "spec", "includedNamespaces")
	require.True(t, limited)
	require.Len(t, projected, MaxDeclaredItems)
	for _, value := range projected {
		require.Len(t, value, MaxFieldBytes)
	}
	require.Nil(t, list(obj, "spec", "absent"))
	require.True(t, (&Snapshot{Coverage: []Coverage{{State: "denied"}, {State: "unknown / unavailable"}}}).CollectionFailed())
	require.False(t, (&Snapshot{Coverage: []Coverage{{State: "empty"}}}).CollectionFailed())
}
