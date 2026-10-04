// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package workspace

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

const (
	storeVersion  = 1
	maxStoreBytes = 1 << 20
	maxScopes     = 64
)

// Store is the versioned, local daily workspace configuration.
type Store struct {
	Version int     `yaml:"version"`
	Active  string  `yaml:"active,omitempty"`
	Scopes  []Scope `yaml:"scopes,omitempty"`
}

var storeMu sync.Mutex

// LoadStore loads strict, bounded YAML. A missing file yields an empty v1 store;
// malformed files are returned as errors instead of being replaced or ignored.
func LoadStore(path string) (Store, error) {
	storeMu.Lock()
	defer storeMu.Unlock()
	root, base, err := openStoreParent(path, false)
	if errors.Is(err, os.ErrNotExist) {
		return Store{Version: storeVersion}, nil
	}
	if err != nil {
		return Store{}, err
	}
	defer root.Close()
	info, err := root.Lstat(base)
	if errors.Is(err, os.ErrNotExist) {
		return Store{Version: storeVersion}, nil
	}
	if err != nil {
		return Store{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxStoreBytes {
		return Store{}, fmt.Errorf("workspace store must be a regular file of at most 1MiB")
	}
	file, err := root.Open(base)
	if err != nil {
		return Store{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return Store{}, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return Store{}, fmt.Errorf("workspace store changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxStoreBytes+1))
	if err != nil {
		return Store{}, err
	}
	if len(data) > maxStoreBytes {
		return Store{}, fmt.Errorf("workspace store exceeds 1MiB")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var store Store
	if err := decoder.Decode(&store); err != nil {
		return Store{}, fmt.Errorf("parse workspace store: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Store{}, fmt.Errorf("workspace store must contain exactly one YAML document")
	}
	return normalizeStore(store)
}

// SaveStore validates everything before writing and atomically replaces a regular
// file with a private 0600 file. Errors preserve the previous configuration.
// Symlink components, symlink targets and concurrent target replacements are
// refused. Directory handles anchor operations throughout the write.
func SaveStore(path string, store Store) error {
	normalized, err := normalizeStore(store)
	if err != nil {
		return err
	}
	data, err := yaml.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("encode workspace store: %w", err)
	}
	if len(data) > maxStoreBytes {
		return fmt.Errorf("workspace store exceeds 1MiB")
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	root, base, err := openStoreParent(path, true)
	if err != nil {
		return err
	}
	defer root.Close()
	parentInfo, err := root.Stat(".")
	if err != nil {
		return err
	}
	if runtime.GOOS != "windows" && parentInfo.Mode().Perm()&0022 != 0 && parentInfo.Mode()&os.ModeSticky == 0 {
		return fmt.Errorf("workspace store directory is writable by other users without sticky protection")
	}
	original, err := replacementTarget(root, base)
	if err != nil {
		return err
	}
	var suffix [16]byte
	if _, err = rand.Read(suffix[:]); err != nil {
		return err
	}
	temporary := ".workspaces-" + hex.EncodeToString(suffix[:]) + ".tmp"
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() {
		// Successful rename already removes this name; cleanup is best-effort on failure.
		_ = root.Remove(temporary)
	}()
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	current, err := replacementTarget(root, base)
	if err != nil {
		return err
	}
	if !sameTarget(original, current) {
		return fmt.Errorf("workspace store changed while saving; previous file was preserved")
	}
	return root.Rename(temporary, base)
}

func normalizeStore(store Store) (Store, error) {
	if store.Version != storeVersion {
		return Store{}, fmt.Errorf("unsupported workspace store version %d (expected %d)", store.Version, storeVersion)
	}
	if len(store.Scopes) > maxScopes {
		return Store{}, fmt.Errorf("at most %d saved workspaces are allowed", maxScopes)
	}
	if err := validateBoundedText("active workspace name", store.Active, 96, false); err != nil {
		return Store{}, err
	}
	out := Store{Version: storeVersion, Active: strings.TrimSpace(store.Active)}
	if err := validateBoundedText("active workspace name", out.Active, 96, false); err != nil {
		return Store{}, err
	}
	names := make(map[string]bool, len(store.Scopes))
	for i := range store.Scopes {
		normalized, err := NormalizeScope(store.Scopes[i])
		if err != nil {
			return Store{}, fmt.Errorf("workspace %q: %w", store.Scopes[i].Name, err)
		}
		if names[normalized.Name] {
			return Store{}, fmt.Errorf("duplicate workspace name %q", normalized.Name)
		}
		names[normalized.Name] = true
		out.Scopes = append(out.Scopes, normalized)
	}
	if out.Active != "" && !names[out.Active] {
		return Store{}, fmt.Errorf("active workspace %q does not exist", out.Active)
	}
	return out, nil
}

func replacementTarget(root *os.Root, name string) (os.FileInfo, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("refusing to replace non-regular workspace store %q", name)
	}
	return info, nil
}

func sameTarget(before, after os.FileInfo) bool {
	if before == nil || after == nil {
		return before == nil && after == nil
	}
	return os.SameFile(before, after) && before.Mode() == after.Mode() && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}

// openStoreParent walks directory handles one component at a time, refusing
// symlinks. It avoids following a symlink swapped into an ancestor during a save.
func openStoreParent(path string, create bool) (*os.Root, string, error) {
	if err := validateBoundedText("workspace store path", path, 4096, true); err != nil {
		return nil, "", err
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return nil, "", fmt.Errorf("workspace store path cannot contain parent traversal")
		}
	}
	if strings.HasSuffix(path, string(filepath.Separator)) || filepath.Base(path) == "." {
		return nil, "", fmt.Errorf("workspace store path must name a file")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, "", err
	}
	volume := filepath.VolumeName(abs)
	anchor := volume + string(filepath.Separator)
	root, err := os.OpenRoot(anchor)
	if err != nil {
		return nil, "", err
	}
	parent := strings.TrimPrefix(filepath.Dir(abs), anchor)
	for _, component := range strings.Split(parent, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		info, statErr := root.Lstat(component)
		if errors.Is(statErr, os.ErrNotExist) && create {
			if mkdirErr := root.Mkdir(component, 0700); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
				root.Close()
				return nil, "", mkdirErr
			}
			info, statErr = root.Lstat(component)
		}
		if statErr != nil {
			root.Close()
			return nil, "", statErr
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			root.Close()
			return nil, "", fmt.Errorf("workspace store directory %q must be a real directory, not a symlink", component)
		}
		next, openErr := root.OpenRoot(component)
		if openErr != nil {
			root.Close()
			return nil, "", openErr
		}
		opened, statErr := next.Stat(".")
		root.Close()
		if statErr != nil || !os.SameFile(info, opened) {
			next.Close()
			return nil, "", fmt.Errorf("workspace store directory changed while opening")
		}
		root = next
	}
	return root, filepath.Base(abs), nil
}
