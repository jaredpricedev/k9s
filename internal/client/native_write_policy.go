// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package client

import (
	"net/http"

	"k8s.io/client-go/rest"
)

// nativeWritePolicy prevents client-go's response-driven Retry-After retry of
// native mutations. A write's first response must reach its operation owner:
// a server failure can leave submission outcome unknown. Read retries remain
// available. This cannot control retries inside an opaque custom transport.
func nativeWritePolicy(config *rest.Config) *rest.Config {
	private := rest.CopyConfig(config)
	private.Wrap(func(transport http.RoundTripper) http.RoundTripper {
		return nativeWriteTransport{transport: transport}
	})
	return private
}

type nativeWriteTransport struct{ transport http.RoundTripper }

func (t nativeWriteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.transport.RoundTrip(request)
	if response == nil {
		return nil, err
	}
	switch request.Method {
	case http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete:
		if response.Header.Get("Retry-After") != "" {
			// Preserve the transport's response and possibly shared header map.
			private := *response
			private.Header = response.Header.Clone()
			private.Header.Del("Retry-After")
			return &private, err
		}
	}
	return response, err
}

// Keep transport unwrapping available to Kubernetes upgrade/dial helpers.
func (t nativeWriteTransport) WrappedRoundTripper() http.RoundTripper { return t.transport }
