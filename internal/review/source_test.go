// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package review

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const (
	sourceTestFilename = "manifest.yaml"
	sourceTestWindows  = "windows"
	sourceTestToken    = "private-fixture-credential"
	sourceTestManifest = "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: api\n"
)

func sourceTestDirectory(t *testing.T) string {
	t.Helper()
	// Some platforms place their temporary directory beneath a system symlink.
	// The source contract requires the actual path, including its ancestors.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return dir
}

func sourceTestFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(sourceTestDirectory(t), sourceTestFilename)
	require.NoError(t, os.WriteFile(path, []byte(content), 0600))
	return path
}

func TestLoadSourceIdentityAndAuthoredStructure(t *testing.T) {
	content := "# explicit input\n---\n" + sourceTestManifest + `spec:
  replicas: 0
  nullable: null
  emptyMap: {}
  emptyList: []
  values: [false, 1.5, "001", 2026-10-04]
  untouched: {password: private-fixture-credential}
---
# intentionally empty document
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: settings
  namespace: team-a
data:
  config: original-value
`
	path := sourceTestFile(t, content)
	before := time.Now().UTC()
	source, err := LoadSource(context.Background(), path)
	require.NoError(t, err)
	digest := sha256.Sum256([]byte(content))
	require.Equal(t, path, source.Identity.Path)
	require.Equal(t, hex.EncodeToString(digest[:]), source.Identity.SHA256)
	require.Equal(t, int64(len(content)), source.Identity.Bytes)
	require.Equal(t, 2, source.Identity.Documents)
	require.False(t, source.Identity.LoadedAt.Before(before))
	require.False(t, source.Identity.LoadedAt.After(time.Now().UTC()))
	require.Equal(t, time.UTC, source.Identity.LoadedAt.Location())
	require.Len(t, source.Objects, 2)
	require.Equal(t, 1, source.Objects[0].Document)
	require.Equal(t, 3, source.Objects[1].Document)
	require.Empty(t, source.Objects[0].Namespace)
	require.Equal(t, "team-a", source.Objects[1].Namespace)
	spec := source.Objects[0].Object["spec"].(map[string]any)
	require.Equal(t, int64(0), spec["replicas"])
	require.Contains(t, spec, "nullable")
	require.Nil(t, spec["nullable"])
	require.Equal(t, map[string]any{}, spec["emptyMap"])
	require.Equal(t, []any{}, spec["emptyList"])
	require.Equal(t, []any{false, 1.5, "001", "2026-10-04"}, spec["values"])
	require.NotContains(t, spec, "imagePullPolicy")
	require.Equal(t, sourceTestToken, spec["untouched"].(map[string]any)["password"])
	require.Equal(t, "original-value", source.Objects[1].Object["data"].(map[string]any)["config"])
	for _, value := range []any{source, source.Objects[0]} {
		encodedJSON, marshalErr := json.Marshal(value)
		require.NoError(t, marshalErr)
		encodedYAML, yamlErr := yaml.Marshal(value)
		require.NoError(t, yamlErr)
		require.NotContains(t, string(encodedJSON), sourceTestToken)
		require.NotContains(t, string(encodedYAML), sourceTestToken)
	}
	unchanged, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, content, string(unchanged))
}

func TestLoadSourceExcludesEverySecretPayloadField(t *testing.T) {
	content := `apiVersion: v1
kind: Secret
metadata:
  name: credential
  namespace: team-a
  labels: {token: private-fixture-credential}
  annotations: {description: private-fixture-credential}
type: private-fixture-credential
data: {password: private-fixture-credential}
stringData: {token: private-fixture-credential}
spec: {extra: private-fixture-credential}
`
	source, err := LoadSource(context.Background(), sourceTestFile(t, content))
	require.NoError(t, err)
	require.Len(t, source.Objects, 1)
	manifest := source.Objects[0]
	require.True(t, manifest.SecretExcluded)
	require.Equal(t, map[string]any{
		"apiVersion": "v1", "kind": sourceSecretKind,
		sourceMetadataKey: map[string]any{sourceNameKey: "credential", sourceNamespaceKey: "team-a"},
	}, manifest.Object)
	encoded, err := json.Marshal(manifest.Object)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), sourceTestToken)
}

func TestLoadSourceRejectsInvalidInputWithoutCredentialErrors(t *testing.T) {
	cases := map[string]string{
		"empty":                "",
		"comments only":        "# " + sourceTestToken + "\n",
		"parser":               sourceTestManifest + "spec: [" + sourceTestToken + "\n",
		"duplicate root key":   sourceTestManifest + "metadata: {name: " + sourceTestToken + "}\n",
		"duplicate nested key": sourceTestManifest + "spec: {password: first, password: " + sourceTestToken + "}\n",
		"anchor":               sourceTestManifest + "spec: &hidden {password: " + sourceTestToken + "}\n",
		"alias":                sourceTestManifest + "spec: {password: *hidden}\n",
		"merge key":            sourceTestManifest + "spec: {<<: {password: " + sourceTestToken + "}}\n",
		"custom scalar tag":    sourceTestManifest + "spec: {password: !hidden " + sourceTestToken + "}\n",
		"custom map tag":       sourceTestManifest + "spec: !hidden {password: " + sourceTestToken + "}\n",
		"custom sequence tag":  sourceTestManifest + "spec: !hidden [" + sourceTestToken + "]\n",
		"nonstring key":        sourceTestManifest + "spec: {12: " + sourceTestToken + "}\n",
		"infinite":             sourceTestManifest + "spec: {value: .inf}\n",
		"nan":                  sourceTestManifest + "spec: {value: .nan}\n",
		"binary":               sourceTestManifest + "spec: {value: !!binary cHJpdmF0ZQ==}\n",
		"sequence root":        "- {password: " + sourceTestToken + "}\n",
		"scalar root":          sourceTestToken,
		"missing kind":         "apiVersion: v1\nmetadata: {name: api}\n",
		"invalid kind":         "apiVersion: v1\nkind: 'Secret/Pod'\nmetadata: {name: api}\n",
		"kind type":            "apiVersion: v1\nkind: 12\nmetadata: {name: api}\n",
		"missing version":      "kind: Pod\nmetadata: {name: api}\n",
		"invalid version":      "apiVersion: apps/v1/extra\nkind: Pod\nmetadata: {name: api}\n",
		"empty group":          "apiVersion: /v1\nkind: Pod\nmetadata: {name: api}\n",
		"missing name":         "apiVersion: v1\nkind: Pod\nmetadata: {generateName: api-}\n",
		"invalid name":         "apiVersion: v1\nkind: Pod\nmetadata: {name: ../api}\n",
		"name type":            "apiVersion: v1\nkind: Pod\nmetadata: {name: 12}\n",
		"empty namespace":      sourceTestManifest + "  namespace: ''\n",
		"namespace type":       sourceTestManifest + "  namespace: 12\n",
		"invalid namespace":    sourceTestManifest + "  namespace: Team_A\n",
		"list":                 "apiVersion: v1\nkind: List\nitems: []\n",
		"typed list":           "apiVersion: v1\nkind: PodList\nmetadata: {name: api}\nitems: []\n",
		"secret list":          "apiVersion: v1\nkind: SecretList\nmetadata: {name: api}\nitems: [{data: {password: " + sourceTestToken + "}}]\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := sourceTestFile(t, content)
			source, err := LoadSource(context.Background(), path)
			require.Error(t, err)
			require.Equal(t, Source{}, source)
			require.NotContains(t, err.Error(), sourceTestToken)
			original, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			require.Equal(t, content, string(original))
		})
	}
}

func TestLoadSourceRejectsDuplicateAPIdentityAcrossVersions(t *testing.T) {
	duplicate := sourceTestManifest + "---\n" + strings.Replace(sourceTestManifest, "apps/v1", "apps/v1beta1", 1)
	source, err := LoadSource(context.Background(), sourceTestFile(t, duplicate))
	require.ErrorContains(t, err, "duplicates an API identity")
	require.Equal(t, Source{}, source)
	distinct := sourceTestManifest + "---\n" + sourceTestManifest + "  namespace: team-a\n---\n" +
		strings.Replace(sourceTestManifest, "apps/v1", "example.io/v1", 1) + "---\n" +
		strings.Replace(sourceTestManifest, "Deployment", "StatefulSet", 1)
	source, err = LoadSource(context.Background(), sourceTestFile(t, distinct))
	require.NoError(t, err)
	require.Len(t, source.Objects, 4)
	role := "apiVersion: rbac.authorization.k8s.io/v1\nkind: Role\nmetadata: {name: 'system:view'}\n"
	source, err = LoadSource(context.Background(), sourceTestFile(t, role))
	require.NoError(t, err)
	require.Equal(t, "system:view", source.Objects[0].Name)
}

func TestLoadSourceBoundsBytesObjectsAndEmptyDocuments(t *testing.T) {
	content := sourceTestManifest + "#" + strings.Repeat("x", MaxSourceBytes-len(sourceTestManifest)-1)
	source, err := LoadSource(context.Background(), sourceTestFile(t, content))
	require.NoError(t, err)
	require.Equal(t, int64(MaxSourceBytes), source.Identity.Bytes)
	source, err = LoadSource(context.Background(), sourceTestFile(t, content+"x"))
	require.ErrorContains(t, err, "1MiB")
	require.Equal(t, Source{}, source)
	var documents strings.Builder
	for i := range MaxManifests {
		fmt.Fprintf(&documents, "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: item-%d}\n---\n", i)
	}
	source, err = LoadSource(context.Background(), sourceTestFile(t, documents.String()))
	require.NoError(t, err)
	require.Len(t, source.Objects, MaxManifests)
	source, err = LoadSource(context.Background(), sourceTestFile(t, documents.String()+sourceTestManifest))
	require.ErrorContains(t, err, "64-object")
	require.Equal(t, Source{}, source)
	source, err = LoadSource(context.Background(), sourceTestFile(t, strings.Repeat("---\n", maxSourceDocuments+1)))
	require.ErrorContains(t, err, "too many YAML documents")
	require.Equal(t, Source{}, source)
}

func TestLoadSourceBoundsFieldsAndDepth(t *testing.T) {
	deep := sourceTestManifest + "spec: " + strings.Repeat("[", maxSourceDepth+1) + "0" + strings.Repeat("]", maxSourceDepth+1)
	_, err := LoadSource(context.Background(), sourceTestFile(t, deep))
	require.ErrorContains(t, err, "nesting limits")
	wide := sourceTestManifest + "spec: [" + strings.Repeat("0,", maxSourceNodes) + "0]\n"
	_, err = LoadSource(context.Background(), sourceTestFile(t, wide))
	require.ErrorContains(t, err, "field or nesting limits")
}

func TestLoadSourceRefusesDirectoriesAndSymlinks(t *testing.T) {
	dir := sourceTestDirectory(t)
	path := filepath.Join(dir, sourceTestFilename)
	require.NoError(t, os.WriteFile(path, []byte(sourceTestManifest), 0600))
	for _, invalid := range []string{dir, filepath.Join(dir, "missing"), "", "bad\npath", strings.Repeat("x", 4097)} {
		source, err := LoadSource(context.Background(), invalid)
		require.Error(t, err)
		require.Equal(t, Source{}, source)
	}
	if runtime.GOOS == sourceTestWindows {
		t.Skip("symlink creation requires privileges on Windows")
	}
	link := filepath.Join(dir, "link.yaml")
	require.NoError(t, os.Symlink(path, link))
	_, err := LoadSource(context.Background(), link)
	require.Error(t, err)
	linkedDir := filepath.Join(dir, "linked-directory")
	require.NoError(t, os.Symlink(dir, linkedDir))
	_, err = LoadSource(context.Background(), filepath.Join(linkedDir, sourceTestFilename))
	require.ErrorContains(t, err, "real directory ancestors")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 3)
}

func TestSourceStableReadRejectsMutationAndReplacement(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(fmt.Sprintf("replacement=%t", replacement), func(t *testing.T) {
			path := sourceTestFile(t, sourceTestManifest)
			handle, err := openSourceFile(context.Background(), path)
			require.NoError(t, err)
			defer handle.close()
			if replacement {
				next := filepath.Join(filepath.Dir(path), "next.yaml")
				require.NoError(t, os.WriteFile(next, []byte(sourceTestManifest), 0600))
				if runtime.GOOS == sourceTestWindows {
					t.Skip("Windows does not replace a file held open for reading")
				}
				require.NoError(t, os.Rename(next, path))
			} else {
				require.NoError(t, os.WriteFile(path, []byte(sourceTestManifest+"spec: {replicas: 2}\n"), 0600))
			}
			require.ErrorContains(t, handle.checkStable(context.Background()), "changed while reading")
		})
	}
}

func TestSourceStableReadRejectsMovedAncestor(t *testing.T) {
	if runtime.GOOS == sourceTestWindows {
		t.Skip("Windows does not rename a directory containing an open source")
	}
	dir := sourceTestDirectory(t)
	parent := filepath.Join(dir, "input")
	require.NoError(t, os.Mkdir(parent, 0700))
	path := filepath.Join(parent, sourceTestFilename)
	require.NoError(t, os.WriteFile(path, []byte(sourceTestManifest), 0600))
	handle, err := openSourceFile(context.Background(), path)
	require.NoError(t, err)
	defer handle.close()
	moved := filepath.Join(dir, "previous")
	require.NoError(t, os.Rename(parent, moved))
	require.NoError(t, os.Symlink(moved, parent))
	require.ErrorContains(t, handle.checkStable(context.Background()), "real directory ancestors")
}

type sourceCancelReader struct{ cancel context.CancelFunc }

func (r sourceCancelReader) Read(data []byte) (int, error) {
	r.cancel()
	return copy(data, sourceTestManifest), io.EOF
}

type sourceErrorReader struct{}

func (sourceErrorReader) Read([]byte) (int, error) {
	return 0, errors.New(sourceTestToken)
}

func TestSourceCancellationAndSafeReadErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source, err := LoadSource(ctx, "missing")
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, Source{}, source)
	objects, err := decodeSource(ctx, []byte(sourceTestManifest))
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, objects)
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	data, err := readSourceBytes(ctx, sourceCancelReader{cancel: cancel})
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, data)
	data, err = readSourceBytes(context.Background(), sourceErrorReader{})
	require.Error(t, err)
	require.NotContains(t, err.Error(), sourceTestToken)
	require.Nil(t, data)
}
