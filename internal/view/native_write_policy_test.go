// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"
)

func TestNativeHTTPWriteOutcomesPreserveProxyUncertainty(t *testing.T) {
	for _, actor := range []string{"typed", "dynamic"} {
		for _, fixture := range []struct {
			name       string
			code       int
			structured bool
			want       operationState
		}{
			{"reported throttling", http.StatusTooManyRequests, true, operationFailed},
			{"proxy throttling", http.StatusTooManyRequests, false, operationUnknown},
			{"proxy conflict", http.StatusConflict, false, operationUnknown},
			{"recognized conflict", http.StatusConflict, true, operationAccepted},
			{"reported server failure", http.StatusServiceUnavailable, true, operationUnknown},
			{"proxy server failure", http.StatusServiceUnavailable, false, operationUnknown},
			{"higher server status", 599, true, operationUnknown},
			{"nonstandard server status", 777, true, operationUnknown},
		} {
			t.Run(actor+"/"+fixture.name, func(t *testing.T) {
				var contacts atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					attempt := contacts.Add(1)
					if fixture.structured && fixture.code == http.StatusConflict && attempt > 1 {
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"fixture-pod","namespace":"apps","uid":"fixture-uid"}}`))
						return
					}
					w.Header().Set("Retry-After", "0")
					if fixture.structured {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(fixture.code)
						reason := metav1.StatusReasonUnknown
						switch fixture.code {
						case http.StatusTooManyRequests:
							reason = metav1.StatusReasonTooManyRequests
						case http.StatusConflict:
							reason = metav1.StatusReasonConflict
						case http.StatusServiceUnavailable:
							reason = metav1.StatusReasonServiceUnavailable
						}
						_ = json.NewEncoder(w).Encode(&metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
							Status: metav1.StatusFailure, Code: int32(fixture.code), Reason: reason})
					} else {
						w.WriteHeader(fixture.code)
						_, _ = w.Write([]byte("unrecognized proxy response"))
					}
				}))
				defer server.Close()
				config := client.NewConfig(genericclioptions.NewConfigFlags(false))
				config.PrepareSessionREST(&rest.Config{Host: server.URL})
				connection, err := client.NewSessionConnection(config)
				require.NoError(t, err)
				defer connection.CloseSession()
				task := startOperationBatch(time.Second, []SelectedResourceTarget{{Name: "fixture-pod"}},
					func(ctx context.Context, target SelectedResourceTarget) error {
						return retryOperationConflict(ctx, func() error {
							operationBeginWrite(ctx)
							patch := []byte(`{"metadata":{"labels":{"review":"true"}}}`)
							if actor == "typed" {
								typed, dialErr := connection.Dial()
								if dialErr != nil {
									return dialErr
								}
								_, writeErr := typed.CoreV1().Pods("apps").Patch(ctx, target.Name, types.MergePatchType, patch, metav1.PatchOptions{})
								return writeErr
							}
							dynamic, dialErr := connection.DynDial()
							if dialErr != nil {
								return dialErr
							}
							pods := dynamic.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace("apps")
							_, writeErr := pods.Patch(ctx, target.Name, types.MergePatchType, patch, metav1.PatchOptions{})
							return writeErr
						})
					}, nil, nil)
				receipt := waitOperationTask(t, task)
				require.Len(t, receipt.Outcomes, 1)
				require.Equal(t, fixture.want, receipt.Outcomes[0].State)
				wantContacts := 1
				if fixture.structured && fixture.code == http.StatusConflict {
					wantContacts = 2
				}
				require.EqualValues(t, wantContacts, contacts.Load())
			})
		}
	}
}
