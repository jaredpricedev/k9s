// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package data_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/derailed/k9s/internal/config/data"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizeFileName(t *testing.T) {
	uu := map[string]struct {
		file, e string
	}{
		"empty": {},
		"plain": {
			file: "bumble-bee-tuna",
			e:    "bumble-bee-tuna",
		},
		"slash": {
			file: "bumble/bee/tuna",
			e:    "bumble-bee-tuna",
		},
		"column": {
			file: "bumble::bee:tuna",
			e:    "bumble-bee-tuna",
		},
		"eks": {
			file: "arn:aws:eks:us-east-1:123456789:cluster/us-east-1-app-dev-common-eks",
			e:    "arn-aws-eks-us-east-1-123456789-cluster-us-east-1-app-dev-common-eks",
		},
	}

	for k := range uu {
		u := uu[k]
		t.Run(k, func(t *testing.T) {
			assert.Equal(t, u.e, data.SanitizeFileName(u.file))
		})
	}
}

func TestHelperInList(t *testing.T) {
	uu := []struct {
		item     string
		list     []string
		expected bool
	}{
		{"a", []string{}, false},
		{"", []string{}, false},
		{"", []string{""}, true},
		{"a", []string{"a", "b", "c", "d"}, true},
		{"z", []string{"a", "b", "c", "d"}, false},
	}

	for _, u := range uu {
		assert.Equal(t, u.expected, slices.Contains(u.list, u.item))
	}
}

func TestEnsureDirPathNone(t *testing.T) {
	const mod = 0744

	root := t.TempDir()
	dir := filepath.Join(root, "created")
	// Directory creation respects the process umask. Compare with an ordinary
	// mkdir instead of sharing a fixed /tmp directory with other packages.
	reference := filepath.Join(root, "reference")
	require.NoError(t, os.Mkdir(reference, mod))
	expected, err := os.Stat(reference)
	require.NoError(t, err)

	path := filepath.Join(dir, "duh.yaml")
	require.NoError(t, data.EnsureDirPath(path, mod))

	p, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, expected.Mode(), p.Mode())
}

func TestEnsureDirPathNoOpt(t *testing.T) {
	var mod os.FileMode = 0744
	dir := filepath.Join(t.TempDir(), "existing")
	require.NoError(t, os.Mkdir(dir, mod))
	expected, err := os.Stat(dir)
	require.NoError(t, err)

	path := filepath.Join(dir, "duh.yaml")
	require.NoError(t, data.EnsureDirPath(path, mod))

	p, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, expected.Mode(), p.Mode())
}
