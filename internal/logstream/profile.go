package logstream

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

func validateRules(rules []string) error {
	if len(rules) > 128 {
		return fmt.Errorf("at most 128 exclusion rules are allowed")
	}
	total := 0
	for _, r := range rules {
		if r == "" {
			return fmt.Errorf("empty exclusion would hide every entry")
		}
		if len(r) > 4096 {
			return fmt.Errorf("exclusion exceeds 4096 bytes")
		}
		total += len(r)
	}
	if total > 65536 {
		return fmt.Errorf("exclusions exceed 64KiB")
	}
	return nil
}

type Scope struct{ Context, Namespace, LabelKey, LabelValue, Kind, Name string }

func ProfileScope(context, namespace string, labels map[string]string, labelKeys []string, kind, name string) Scope {
	s := Scope{Context: context, Namespace: namespace}
	if len(labelKeys) == 0 {
		labelKeys = []string{"app.kubernetes.io/name", "app"}
	}
	for _, key := range labelKeys {
		if value := labels[key]; value != "" {
			s.LabelKey = key
			s.LabelValue = value
			return s
		}
	}
	s.Kind = kind
	s.Name = name
	return s
}
func TeamRules(annotation string) ([]string, error) {
	if strings.TrimSpace(annotation) == "" {
		return nil, nil
	}
	if len(annotation) > 128<<10 {
		return nil, fmt.Errorf("team log-noise annotation exceeds 128KiB")
	}
	if !strings.HasPrefix(strings.TrimSpace(annotation), "[") {
		return nil, fmt.Errorf("k9plus.io/log-noise must be a JSON array of strings")
	}
	var rules []string
	if err := json.Unmarshal([]byte(annotation), &rules); err != nil {
		return nil, fmt.Errorf("k9plus.io/log-noise: %w", err)
	}
	if err := validateRules(rules); err != nil {
		return nil, err
	}
	return rules, nil
}
func MergeRules(team, local []string) ([]string, error) {
	if err := validateRules(team); err != nil {
		return nil, err
	}
	if err := validateRules(local); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, group := range [][]string{team, local} {
		for _, r := range group {
			if !seen[r] {
				out = append(out, r)
				seen[r] = true
			}
		}
	}
	return out, validateRules(out)
}

type profileRecord struct {
	Scope Scope
	Rules []string
}

var profileMu sync.Mutex

func readProfiles(path string) ([]profileRecord, error) {
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > 8<<20 {
		return nil, fmt.Errorf("profile must be a regular JSON file of at most 8MiB")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var records []profileRecord
	if err = json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("parse noise profiles: %w", err)
	}
	if len(records) > 1024 {
		return nil, fmt.Errorf("at most 1024 workload profiles are allowed")
	}
	for _, r := range records {
		if validationErr := validateRules(r.Rules); validationErr != nil {
			return nil, validationErr
		}
	}
	return records, nil
}

//nolint:gocritic // Scope is an immutable comparable value in this public API.
func LoadProfile(path string, scope Scope) ([]string, error) {
	profileMu.Lock()
	defer profileMu.Unlock()
	records, err := readProfiles(path)
	if err != nil {
		return nil, err
	}
	for _, r := range records {
		if r.Scope == scope {
			return append([]string(nil), r.Rules...), nil
		}
	}
	return nil, nil
}

//nolint:gocritic // Scope is an immutable comparable value in this public API.
func SaveProfile(path string, scope Scope, rules []string) error {
	if err := validateRules(rules); err != nil {
		return err
	}
	profileMu.Lock()
	defer profileMu.Unlock()
	records, err := readProfiles(path)
	if err != nil {
		return err
	}
	found := false
	for i := range records {
		if records[i].Scope == scope {
			records[i].Rules = append([]string(nil), rules...)
			found = true
			break
		}
	}
	if !found {
		if len(records) >= 1024 {
			return fmt.Errorf("at most 1024 workload profiles are allowed")
		}
		records = append(records, profileRecord{scope, append([]string(nil), rules...)})
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > 8<<20 {
		return fmt.Errorf("noise profiles exceed 8MiB")
	}
	return atomicPrivateWrite(path, data)
}
func atomicPrivateWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".logstream-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
