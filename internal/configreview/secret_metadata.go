// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package configreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/rest"
)

const metadataAccept = "application/json;as=PartialObjectMetadata;g=meta.k8s.io;v=v1"

// NewSecretMetadataReader deliberately has no full-object Accept fallback.
// Unsupported negotiation is an observation gap. Values and annotations are
// never returned to the caller, including when a server ignores negotiation.
func NewSecretMetadataReader(config *rest.Config) (SecretMetadata, error) {
	if config == nil {
		return nil, errors.New("captured API transport unavailable")
	}
	cfg := rest.CopyConfig(config)
	cfg.Timeout = 3 * time.Second
	client, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return nil, errors.New("captured metadata transport unavailable")
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("metadata redirects are unsupported")
	}
	base, err := url.Parse(cfg.Host)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil {
		return nil, errors.New("captured API endpoint unavailable")
	}
	return func(ctx context.Context, namespace, name string) (*metav1.PartialObjectMetadata, error) {
		if len(validation.IsDNS1123Label(namespace)) != 0 || len(validation.IsDNS1123Subdomain(name)) != 0 {
			return nil, errors.New("one explicit valid Secret namespace/name is required")
		}
		endpoint := *base
		endpoint.Path = strings.TrimSuffix(base.Path, "/") + "/api/v1/namespaces/" + namespace + "/secrets/" + name
		endpoint.RawPath, endpoint.RawQuery, endpoint.Fragment = "", "", ""
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), http.NoBody)
		if err != nil {
			return nil, errors.New("metadata request unavailable")
		}
		request.Header.Set("Accept", metadataAccept)
		response, err := client.Do(request)
		if err != nil {
			return nil, errors.New("strict metadata read unavailable; no full-object fallback")
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, metadataStatusError(response, name)
		}
		return decodeMetadata(response.Body, namespace, name)
	}, nil
}

func metadataStatusError(response *http.Response, name string) error {
	code := response.StatusCode
	resource := schema.GroupResource{Resource: "secrets"}
	switch code {
	case http.StatusForbidden, http.StatusUnauthorized:
		return apierrors.NewForbidden(resource, name, errors.New("metadata read denied"))
	case http.StatusNotFound:
		// A proxy/unsupported-endpoint 404 is not evidence of a missing Secret.
		if metadataNamedNotFound(response.Body, name) {
			return apierrors.NewNotFound(resource, name)
		}
		return errors.New("metadata endpoint returned unverified 404; named object existence unknown")
	default:
		return fmt.Errorf("strict metadata negotiation unavailable (HTTP %d); no fallback requested", code)
	}
}

func metadataNamedNotFound(body io.Reader, name string) bool {
	const maxStatusBytes = 32 * 1024
	raw, err := io.ReadAll(io.LimitReader(body, maxStatusBytes+1))
	if err != nil || len(raw) > maxStatusBytes {
		return false
	}
	var status struct {
		APIVersion, Kind, Reason string
		Code                     int
		Details                  struct{ Name, Kind, Group string }
	}
	return json.Unmarshal(raw, &status) == nil && status.APIVersion == "v1" && status.Kind == "Status" &&
		status.Code == http.StatusNotFound && status.Reason == "NotFound" && status.Details.Name == name &&
		status.Details.Kind == "secrets" && status.Details.Group == ""
}

func decodeMetadata(body io.Reader, namespace, name string) (*metav1.PartialObjectMetadata, error) {
	const maxMetadataBytes = 512 * 1024
	raw, err := io.ReadAll(io.LimitReader(body, maxMetadataBytes+1))
	if err != nil || len(raw) > maxMetadataBytes {
		return nil, errors.New("metadata response unavailable or exceeded its bound")
	}
	var envelope struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name, Namespace, UID, ResourceVersion string
			Annotations                           map[string]json.RawMessage
		} `json:"metadata"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.APIVersion != "meta.k8s.io/v1" || envelope.Kind != "PartialObjectMetadata" {
		return nil, errors.New("server did not return PartialObjectMetadata; full objects are rejected")
	}
	meta := &envelope.Metadata
	if meta.Name != name || meta.Namespace != namespace || meta.UID == "" {
		return nil, errors.New("metadata response identity was not verified")
	}
	keys := make(map[string]string, min(len(meta.Annotations), MaxKeys))
	for key := range meta.Annotations {
		if len(keys) >= MaxKeys {
			break
		}
		keys[key] = ""
	}
	return &metav1.PartialObjectMetadata{
		TypeMeta: metav1.TypeMeta{APIVersion: "meta.k8s.io/v1", Kind: "PartialObjectMetadata"},
		ObjectMeta: metav1.ObjectMeta{Name: meta.Name, Namespace: meta.Namespace, UID: types.UID(meta.UID),
			ResourceVersion: meta.ResourceVersion, Annotations: keys},
	}, nil
}
