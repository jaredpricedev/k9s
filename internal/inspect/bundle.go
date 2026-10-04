// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package inspect

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/logstream"
)

const BundleVersion = 1
const MaxBundleBytes = 2 << 20

// Bundle is offline evidence. Importing it never applies resources or contacts
// its recorded context. Every observation keeps its own source, identity/time
// and completeness. Redaction is heuristic, not a confidentiality guarantee.
type Bundle struct {
	Version      int           `json:"version"`
	CreatedAt    time.Time     `json:"created_at"`
	Observations []Observation `json:"observations"`
	Notes        []string      `json:"notes,omitempty"`
	Snippets     []Snippet     `json:"snippets,omitempty"`
	Limits       []string      `json:"limits"`
}

type Snippet struct {
	Source     string    `json:"source"`
	ObservedAt time.Time `json:"observed_at"`
	Text       string    `json:"text"`
	Limits     string    `json:"limits"`
}

func NewBundle(observations []Observation) Bundle {
	return Bundle{Version: BundleVersion, CreatedAt: time.Now().UTC(), Observations: observations,
		Limits: []string{"Explicit selected evidence; not an atomic cluster snapshot or a complete history.",
			"Secret bodies and credential-shaped fields excluded; text redaction is heuristic. Review before sharing.",
			"Import is offline inspection; no resources are applied and no recorded context is contacted."}}
}

// SafeBundle returns private copies for preview, export and import. Size limits
// fail explicitly, preserving the distinction between missing evidence and an
// empty observation. Re-sanitizing imported content prevents terminal controls.
//
//nolint:gocritic // Value API returns an independent sanitized bundle; slice contents are copied below.
func SafeBundle(b Bundle) (Bundle, error) {
	if b.Version != BundleVersion {
		return Bundle{}, fmt.Errorf("unsupported evidence bundle version %d (supported: %d)", b.Version, BundleVersion)
	}
	if b.CreatedAt.IsZero() {
		return Bundle{}, errors.New("bundle creation time is required")
	}
	if len(b.Observations) > 8 || len(b.Snippets) > 20 || len(b.Notes) > 64 || len(b.Limits) > 32 {
		return Bundle{}, errors.New("bundle exceeds 8 observations, 20 snippets, 64 notes or 32 limits")
	}
	if len(b.Observations) == 0 {
		return Bundle{}, errors.New("bundle requires at least one named observation")
	}
	result := b
	result.Observations = make([]Observation, len(b.Observations))
	for i := range b.Observations {
		observation := &b.Observations[i]
		if observation.Source == "" || observation.ObservedAt.IsZero() || observation.Identity.Name == "" || observation.Identity.GVR == "" {
			return Bundle{}, errors.New("each observation requires a source, observation time and resource identity")
		}
		result.Observations[i] = SafeObservation(*observation)
		cleaned := &result.Observations[i]
		if cleaned.Identity.Name == "" || cleaned.Identity.GVR == "" || logstream.SafeText(observation.Source) == "" {
			return Bundle{}, errors.New("observation source and resource identity must remain named after sanitization")
		}
	}
	clean := func(values []string, maximum int) ([]string, error) {
		result := make([]string, len(values))
		for i, value := range values {
			if len(value) > maximum {
				return nil, fmt.Errorf("text exceeds %d bytes", maximum)
			}
			result[i] = logstream.SafeText(value)
		}
		return result, nil
	}
	var err error
	if result.Notes, err = clean(b.Notes, 16<<10); err != nil {
		return Bundle{}, err
	}
	if result.Limits, err = clean(b.Limits, 8<<10); err != nil {
		return Bundle{}, err
	}
	result.Snippets = make([]Snippet, len(b.Snippets))
	for i, snippet := range b.Snippets {
		if snippet.Source == "" || snippet.ObservedAt.IsZero() || len(snippet.Text) > 8<<10 || len(snippet.Source) > 4096 || len(snippet.Limits) > 4096 {
			return Bundle{}, errors.New("snippets require a named source/time and at most 8 KiB text")
		}
		snippet.Source = logstream.SafeText(snippet.Source)
		if snippet.Source == "" {
			return Bundle{}, errors.New("snippet source must remain named after sanitization")
		}
		snippet.Text = logstream.SafeText(snippet.Text)
		snippet.Limits = logstream.SafeText(snippet.Limits)
		result.Snippets[i] = snippet
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return Bundle{}, err
	}
	if len(encoded)+1 > MaxBundleBytes {
		return Bundle{}, errors.New("sanitized bundle exceeds 2 MiB; select less evidence")
	}
	return result, nil
}

//nolint:gocritic // Encoding accepts a value snapshot and never mutates caller evidence.
func EncodeBundle(b Bundle, markdown bool) ([]byte, error) {
	safe, err := SafeBundle(b)
	if err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(safe, "", "  ")
	if err != nil {
		return nil, err
	}
	if markdown {
		encoded = []byte("# k9+ investigation evidence\n\n" +
			"Version 1. Offline inspection only. Review heuristic redaction before sharing.\n\n" +
			"```json k9plus-evidence-v1\n" + string(encoded) + "\n```\n")
	}
	if len(encoded)+1 > MaxBundleBytes {
		return nil, errors.New("formatted bundle exceeds 2 MiB; select less evidence")
	}
	return append(encoded, '\n'), nil
}

func DecodeBundle(r io.Reader) (Bundle, error) {
	encoded, err := io.ReadAll(io.LimitReader(r, MaxBundleBytes+1))
	if err != nil {
		return Bundle{}, err
	}
	if len(encoded) > MaxBundleBytes {
		return Bundle{}, errors.New("bundle exceeds 2 MiB")
	}
	encoded = bytes.TrimSpace(encoded)
	if bytes.HasPrefix(encoded, []byte("# k9+ investigation evidence")) {
		marker := []byte("```json k9plus-evidence-v1\n")
		_, body, ok := bytes.Cut(encoded, marker)
		if !ok {
			return Bundle{}, errors.New("Markdown evidence JSON block missing")
		}
		body, suffix, ok := bytes.Cut(body, []byte("\n```"))
		if !ok || len(bytes.TrimSpace(suffix)) != 0 {
			return Bundle{}, errors.New("malformed Markdown evidence block")
		}
		encoded = body
	}
	d := json.NewDecoder(bytes.NewReader(encoded))
	d.DisallowUnknownFields()
	var b Bundle
	if err := d.Decode(&b); err != nil {
		return Bundle{}, fmt.Errorf("invalid evidence bundle: %w", err)
	}
	var trailing any
	if err := d.Decode(&trailing); err != io.EOF {
		return Bundle{}, errors.New("unexpected content after evidence bundle")
	}
	return SafeBundle(b)
}

// SaveBundle requires a new absolute .json/.md destination. O_EXCL refuses
// overwrite and symlinks, and mode 0600 protects the created evidence file.
//
//nolint:gocritic // Saving accepts a value snapshot and never mutates caller evidence.
func SaveBundle(path string, b Bundle) error {
	if !filepath.IsAbs(path) {
		return errors.New("choose an absolute export path")
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".json" && ext != ".md" {
		return errors.New("export filename must end in .json or .md")
	}
	encoded, err := EncodeBundle(b, ext == ".md")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(encoded)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return errors.Join(writeErr, closeErr)
	}
	return nil
}

func ReadBundle(path string) (Bundle, error) {
	if !filepath.IsAbs(path) {
		return Bundle{}, errors.New("choose an absolute import path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return Bundle{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxBundleBytes {
		return Bundle{}, errors.New("bundle must be a regular file of at most 2 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return Bundle{}, err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return Bundle{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxBundleBytes {
		return Bundle{}, errors.New("bundle must be a regular file of at most 2 MiB")
	}
	return DecodeBundle(f)
}

//nolint:gocritic // Preview accepts a value snapshot and never mutates caller evidence.
func BundlePreview(b Bundle) (string, error) {
	safe, err := SafeBundle(b)
	if err != nil {
		return "", err
	}
	var text strings.Builder
	text.WriteString("EVIDENCE PREVIEW\nExplicit selected observations; offline import.\n" +
		"Secret bodies excluded; redaction is heuristic. Review every included field before sharing.\n")
	fmt.Fprintf(&text, "Created: %s\n", safe.CreatedAt.UTC().Format(time.RFC3339))
	for i := range safe.Observations {
		o := &safe.Observations[i]
		fmt.Fprintf(&text, "\nRESOURCE %s %s/%s\nContext: %s\nUID: %s\nSource: %s\nObserved: %s\nState: %s\n%s\n", o.Identity.GVR, o.Identity.Namespace, o.Identity.Name,
			o.Identity.Context, o.Identity.UID, o.Source, o.ObservedAt.UTC().Format(time.RFC3339), o.State, o.Reason)
		for _, limit := range o.Limits {
			fmt.Fprintf(&text, "Limit: %s\n", limit)
		}
	}
	text.WriteString("\nNOTES\n")
	if len(safe.Notes) == 0 {
		text.WriteString("No notes added. Press n to add a note.\n")
	}
	for _, note := range safe.Notes {
		fmt.Fprintf(&text, "%s\n", note)
	}
	for _, snippet := range safe.Snippets {
		fmt.Fprintf(&text, "\nSELECTED EVIDENCE\nSource: %s\nObserved/selected: %s\nLimit: %s\n%s\n",
			snippet.Source, snippet.ObservedAt.UTC().Format(time.RFC3339), snippet.Limits, snippet.Text)
	}
	text.WriteString("\nCOMPLETENESS\n")
	for _, limit := range safe.Limits {
		fmt.Fprintf(&text, "%s\n", limit)
	}
	for i := range safe.Observations {
		o := &safe.Observations[i]
		if o.Object == nil {
			continue
		}
		encoded, err := json.MarshalIndent(o.Object, "", "  ")
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&text, "\nINCLUDED RESOURCE SNAPSHOT %s/%s\n%s\n", o.Identity.Namespace, o.Identity.Name, encoded)
	}
	if text.Len() > MaxComparisonText {
		return "", errors.New("preview exceeds 256 KiB; select less evidence before saving")
	}
	return text.String(), nil
}
