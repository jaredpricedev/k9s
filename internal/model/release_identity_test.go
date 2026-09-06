// SPDX-License-Identifier: Apache-2.0
// Copyright k9+ contributors
// Modified for k9+; see NOTICE.

package model

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type releaseTransport func(*http.Request) (*http.Response, error)

func (f releaseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLatestReleaseUsesForkTagAndHandlesUnpublishedApp(t *testing.T) {
	previous := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = previous })
	for _, tt := range []struct {
		name, body, want string
		status           int
		wantErr          bool
	}{
		{"release tag, not display title", `{"name":"k9+ launch","tag_name":"v1.0.0"}`, "v1.0.0", 200, false},
		{"no release yet", `{"message":"Not Found"}`, "", 404, false},
		{"rate limited", `{"message":"rate limited"}`, "", 403, true},
		{"malformed tag", `{"tag_name":false}`, "", 200, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			http.DefaultClient = &http.Client{Transport: releaseTransport(func(r *http.Request) (*http.Response, error) {
				assert.Equal(t, "https://api.github.com/repos/jaredpricedev/k9s/releases/latest", r.URL.String())
				return &http.Response{StatusCode: tt.status, Body: io.NopCloser(strings.NewReader(tt.body)), Header: make(http.Header)}, nil
			})}
			value, err := fetchLatestRev()
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, value)
		})
	}
}
