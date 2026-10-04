// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package workspace

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

const fixtureWindows = "windows"

func exampleStore() Store {
	return Store{Version: 1, Active: fixtureWorkspaceName, Scopes: []Scope{exampleScope()}}
}

func TestStoreRoundTripAndPrivateAtomicReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "workspaces.yaml")
	missing, err := LoadStore(path)
	if err != nil || !reflect.DeepEqual(missing, Store{Version: 1}) {
		t.Fatalf("missing store = %#v, %v", missing, err)
	}
	want := exampleStore()
	if err = SaveStore(path, want); err != nil {
		t.Fatal(err)
	}
	first, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != fixtureWindows && first.Mode().Perm() != 0600 {
		t.Fatalf("store mode = %o", first.Mode().Perm())
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != fixtureWindows && parent.Mode().Perm() != 0700 {
		t.Fatalf("created directory mode = %o", parent.Mode().Perm())
	}
	got, err := LoadStore(path)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("loaded = %#v, %v; want %#v", got, err, want)
	}
	want.Scopes[0].Name = "Changed"
	want.Active = "Changed"
	if err = SaveStore(path, want); err != nil {
		t.Fatal(err)
	}
	second, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(first, second) {
		t.Fatal("save overwrote original inode instead of atomically replacing it")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 || entries[0].Name() != "workspaces.yaml" {
		t.Fatalf("temporary files remain: %#v %v", entries, err)
	}
}

func TestStoreStrictYAMLAndBounds(t *testing.T) {
	cases := map[string]string{
		"empty":                    "",
		"missing version":          "scopes: []\n",
		"past version":             "version: 0\n",
		"future version":           "version: 2\n",
		"unknown field":            "version: 1\nunknown: true\n",
		"duplicate field":          "version: 1\nversion: 1\n",
		"multiple documents":       "version: 1\n---\nversion: 1\n",
		"empty second document":    "version: 1\n---\n",
		"unknown scope field":      "version: 1\nscopes:\n  - name: x\n    context: y\n    namespaces: [default]\n    unknown: true\n",
		fixtureInvalidSelectorCase: "version: 1\nscopes:\n  - name: x\n    context: y\n    namespaces: [default]\n    labelSelector: 'app in ('\n",
		"empty namespace":          "version: 1\nscopes:\n  - name: x\n    context: y\n    namespaces: []\n",
		"oversize":                 "version: 1\n#" + strings.Repeat("x", maxStoreBytes),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "workspaces.yaml")
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadStore(path); err == nil {
				t.Fatal("invalid YAML store accepted")
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != content {
				t.Fatal("loading invalid YAML modified the original file")
			}
		})
	}
}

func TestSaveStoreValidationPreservesLastFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspaces.yaml")
	if err := SaveStore(path, exampleStore()); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*Store){
		"version":                  func(s *Store) { s.Version = 2 },
		"missing active":           func(s *Store) { s.Active = "Missing" },
		"duplicate names":          func(s *Store) { s.Scopes = append(s.Scopes, s.Scopes[0]) },
		"namespace widening":       func(s *Store) { s.Scopes[0].Namespaces = []string{"*"} },
		fixtureInvalidSelectorCase: func(s *Store) { s.Scopes[0].LabelSelector = fixtureInvalidSelector },
		"scope count": func(s *Store) {
			s.Scopes = nil
			for i := range 65 {
				scope := exampleScope()
				scope.Name = fmt.Sprintf("scope-%d", i)
				s.Scopes = append(s.Scopes, scope)
			}
		},
		"serialized size": func(s *Store) {
			s.Active = ""
			s.Scopes = nil
			for i := range 64 {
				scope := exampleScope()
				scope.Name = fmt.Sprintf("scope-%d", i)
				scope.Searches = nil
				for j := range 32 {
					scope.Searches = append(scope.Searches, SavedSearch{Name: fmt.Sprintf("search-%d", j), Query: strings.Repeat("q", 1024)})
				}
				s.Scopes = append(s.Scopes, scope)
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			store := exampleStore()
			change(&store)
			if err := SaveStore(path, store); err == nil {
				t.Fatal("invalid store saved")
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatal("failed save modified last valid file")
			}
		})
	}
}

func TestStoreRejectsSymlinksAndNonRegularPaths(t *testing.T) {
	if runtime.GOOS == fixtureWindows {
		t.Skip("symlinks require host permission on Windows")
	}
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside.yaml")
	original := []byte("version: 1\n")
	if err := os.WriteFile(outside, original, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "workspaces.yaml")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadStore(link); err == nil {
		t.Fatal("symlink store loaded")
	}
	if err := SaveStore(link, exampleStore()); err == nil {
		t.Fatal("symlink store replaced")
	}
	got, err := os.ReadFile(outside)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatal("symlink target modified")
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink replaced")
	}
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0700); err != nil {
		t.Fatal(err)
	}
	linkedDir := filepath.Join(dir, "linked")
	if err := os.Symlink(realDir, linkedDir); err != nil {
		t.Fatal(err)
	}
	ancestor := filepath.Join(linkedDir, "new", "workspaces.yaml")
	if _, err := LoadStore(ancestor); err == nil {
		t.Fatal("symlink ancestor accepted as missing store")
	}
	if err := SaveStore(ancestor, exampleStore()); err == nil {
		t.Fatal("saved through symlink ancestor")
	}
	if _, err := os.Stat(filepath.Join(realDir, "new")); !os.IsNotExist(err) {
		t.Fatal("symlink ancestor target changed")
	}
	if _, err := LoadStore(realDir); err == nil {
		t.Fatal("directory store loaded")
	}
	if err := SaveStore(realDir, exampleStore()); err == nil {
		t.Fatal("directory store replaced")
	}
	blocked := filepath.Join(outside, "child.yaml")
	if err := SaveStore(blocked, exampleStore()); err == nil {
		t.Fatal("saved through regular-file ancestor")
	}
}

func TestStoreRejectsUnsafePathStrings(t *testing.T) {
	for _, path := range []string{"", ".", "..", "workspaces/../config.yaml", "bad\x00.yaml", "bad\n.yaml", "directory/", strings.Repeat("x", 4097)} {
		t.Run(fmt.Sprintf("%q", path), func(t *testing.T) {
			if _, err := LoadStore(path); err == nil {
				t.Fatal("unsafe load path accepted")
			}
			if err := SaveStore(path, exampleStore()); err == nil {
				t.Fatal("unsafe save path accepted")
			}
		})
	}
}

func TestStoreTargetReplacementDetection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "workspaces.yaml")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	original, err := replacementTarget(root, "workspaces.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(path, filepath.Join(dir, "previous.yaml")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	replacement, err := replacementTarget(root, "workspaces.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if sameTarget(original, replacement) || sameTarget(original, nil) || sameTarget(nil, replacement) || !sameTarget(nil, nil) {
		t.Fatal("unsafe replacement was considered the original target")
	}
}

func TestSaveStoreRejectsUnsafeParentPermissions(t *testing.T) {
	if runtime.GOOS == fixtureWindows {
		t.Skip("Unix directory write permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "workspaces.yaml")
	if err := os.WriteFile(path, []byte("version: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	if err := SaveStore(path, exampleStore()); err == nil {
		t.Fatal("store replaced in a directory where other users can swap the tempfile")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "version: 1\n" {
		t.Fatal("failed save changed original store")
	}
}
