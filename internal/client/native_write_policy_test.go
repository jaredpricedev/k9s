// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const (
	policyPodName    = "policy-pod"
	policyLoadedMode = "loaded"
)

type policyObserverTransport struct {
	transport http.RoundTripper
	headers   chan http.Header
}

func (t policyObserverTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	private := request.Clone(request.Context())
	private.Header.Set("X-Existing-Wrapper", "retained")
	response, err := t.transport.RoundTrip(private)
	if response != nil {
		t.headers <- response.Header
	}
	return response, err
}

func policyConnection(t *testing.T, mode, host string, headers chan http.Header) *APIClient {
	t.Helper()
	flags := genericclioptions.NewConfigFlags(false)
	*flags.APIServer = host
	path := filepath.Join(t.TempDir(), "config")
	raw := clientcmdapi.NewConfig()
	raw.Clusters["fixture"] = &clientcmdapi.Cluster{Server: host}
	raw.Contexts["fixture"] = &clientcmdapi.Context{Cluster: "fixture"}
	raw.CurrentContext = "fixture"
	require.NoError(t, clientcmd.WriteToFile(*raw, path))
	*flags.KubeConfig = path
	wrap := func(transport http.RoundTripper) http.RoundTripper {
		return policyObserverTransport{transport: transport, headers: headers}
	}
	config := NewConfig(flags)
	if mode != policyLoadedMode {
		config.PrepareSessionREST(&rest.Config{Host: host, WrapTransport: wrap})
	}
	if mode == "session" {
		connection, err := NewSessionConnection(config)
		require.NoError(t, err)
		t.Cleanup(connection.CloseSession)
		return connection
	}
	return &APIClient{config: config, connOK: true}
}

func policyRequest(ctx context.Context, connection *APIClient, actor, method string) error {
	if actor == "typed" {
		clientset, err := connection.Dial()
		if err != nil {
			return err
		}
		pods := clientset.CoreV1().Pods("apps")
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: policyPodName, Namespace: "apps", UID: "policy-uid"}}
		switch method {
		case http.MethodPost:
			_, err = pods.Create(ctx, pod, metav1.CreateOptions{})
		case http.MethodPatch:
			_, err = pods.Patch(ctx, policyPodName, types.MergePatchType, []byte(`{"metadata":{"labels":{"review":"true"}}}`), metav1.PatchOptions{})
		case http.MethodPut:
			_, err = pods.Update(ctx, pod, metav1.UpdateOptions{})
		case http.MethodDelete:
			err = pods.Delete(ctx, policyPodName, metav1.DeleteOptions{})
		default:
			_, err = pods.Get(ctx, policyPodName, metav1.GetOptions{})
		}
		return err
	}
	reader, err := connection.DynDial()
	if err != nil {
		return err
	}
	pods := reader.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace("apps")
	pod := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{
		"name": policyPodName, "namespace": "apps", "uid": "policy-uid"}}}
	switch method {
	case http.MethodPost:
		_, err = pods.Create(ctx, pod, metav1.CreateOptions{})
	case http.MethodPatch:
		_, err = pods.Patch(ctx, policyPodName, types.MergePatchType, []byte(`{"metadata":{"labels":{"review":"true"}}}`), metav1.PatchOptions{})
	case http.MethodPut:
		_, err = pods.Update(ctx, pod, metav1.UpdateOptions{})
	case http.MethodDelete:
		err = pods.Delete(ctx, policyPodName, metav1.DeleteOptions{})
	default:
		_, err = pods.Get(ctx, policyPodName, metav1.GetOptions{})
	}
	return err
}

func TestNativeWriteResponsesReachOwnerOnceAndReadsStillRetry(t *testing.T) {
	for _, mode := range []string{policyLoadedMode, "prepared", "session"} {
		for _, actor := range []string{"typed", "dynamic"} {
			for _, code := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
				for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete, http.MethodGet} {
					t.Run(fmt.Sprintf("%s/%s/%d/%s", mode, actor, code, method), func(t *testing.T) {
						var contacts atomic.Int32
						headers := make(chan http.Header, 12)
						server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
							assert.Equal(t, method, request.Method)
							if mode != policyLoadedMode {
								assert.Equal(t, "retained", request.Header.Get("X-Existing-Wrapper"))
							}
							assert.True(t, request.URL.Path == "/api/v1/namespaces/apps/pods" || request.URL.Path == "/api/v1/namespaces/apps/pods/"+policyPodName)
							w.Header().Set("Content-Type", "application/json")
							if contacts.Add(1) > 1 && method == http.MethodGet {
								_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"policy-pod","namespace":"apps","uid":"policy-uid"}}`))
								return
							}
							w.Header().Set("Retry-After", "0")
							w.WriteHeader(code)
							reason := metav1.StatusReasonTooManyRequests
							if code == http.StatusServiceUnavailable {
								reason = metav1.StatusReasonServiceUnavailable
							}
							_ = json.NewEncoder(w).Encode(&metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
								Status: metav1.StatusFailure, Code: int32(code), Reason: reason, Message: "fixture failure retained",
								Details: &metav1.StatusDetails{Name: policyPodName, Kind: "pods", UID: "policy-uid", RetryAfterSeconds: 7}})
						}))
						defer server.Close()
						connection := policyConnection(t, mode, server.URL, headers)
						ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
						defer cancel()
						err := policyRequest(ctx, connection, actor, method)
						if method == http.MethodGet {
							require.NoError(t, err)
							require.EqualValues(t, 2, contacts.Load())
						} else {
							require.Error(t, err)
							require.EqualValues(t, 1, contacts.Load())
							var status apierrors.APIStatus
							require.ErrorAs(t, err, &status)
							require.EqualValues(t, code, status.Status().Code)
							require.Equal(t, "fixture failure retained", status.Status().Message)
							require.Equal(t, policyPodName, status.Status().Details.Name)
							require.Equal(t, "pods", status.Status().Details.Kind)
							require.Equal(t, types.UID("policy-uid"), status.Status().Details.UID)
							require.EqualValues(t, 7, status.Status().Details.RetryAfterSeconds)
						}
						// The inner transport's original response header remains intact.
						if mode != policyLoadedMode {
							select {
							case header := <-headers:
								require.Equal(t, "0", header.Get("Retry-After"))
							case <-ctx.Done():
								t.Fatal("existing wrapper did not observe the response")
							}
						}
					})
				}
			}
		}
	}
}
