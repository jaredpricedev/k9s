// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package review

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
	pathvalidation "k8s.io/apimachinery/pkg/api/validation/path"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	maxSourceDocuments = MaxManifests * 4
	maxSourceNodes     = 10000
	maxSourceDepth     = 32
	sourceReadChunk    = 8192
	sourceMappingTag   = "!!map"
	sourceSequenceTag  = "!!seq"
	sourceStringTag    = "!!str"
	sourceNullTag      = "!!null"
	sourceSecretKind   = "Secret"
	sourceMetadataKey  = "metadata"
	sourceNamespaceKey = "namespace"
	sourceNameKey      = "name"
)

// LoadSource reads one explicitly named local file. It does not walk manifest
// directories, render templates, resolve namespaces or execute providers.
// Non-Secret maps retain authored fields for comparison; presentation must use
// the review's safe projection. Errors never include raw YAML parser messages.
func LoadSource(ctx context.Context, path string) (Source, error) {
	if err := ctx.Err(); err != nil {
		return Source{}, err
	}
	handle, err := openSourceFile(ctx, path)
	if err != nil {
		return Source{}, err
	}
	defer handle.close()
	data, err := readSourceBytes(ctx, handle.file)
	if err != nil {
		return Source{}, err
	}
	if stableErr := handle.checkStable(ctx); stableErr != nil {
		return Source{}, stableErr
	}
	objects, err := decodeSource(ctx, data)
	if err != nil {
		return Source{}, err
	}
	if stableErr := handle.checkStable(ctx); stableErr != nil {
		return Source{}, stableErr
	}
	digest := sha256.Sum256(data)
	return Source{
		Identity: SourceIdentity{Path: handle.path, SHA256: hex.EncodeToString(digest[:]), Bytes: int64(len(data)),
			Documents: len(objects), LoadedAt: time.Now().UTC()},
		Objects: objects,
	}, nil
}

type sourceFile struct {
	file       *os.File
	root       *os.Root
	info       os.FileInfo
	path, name string
}

func (s *sourceFile) close() {
	_ = s.file.Close()
	_ = s.root.Close()
}

func openSourceFile(ctx context.Context, path string) (*sourceFile, error) {
	if path == "" || len(path) > 4096 || !sourcePrintable(path) {
		return nil, errors.New("manifest source path is invalid")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("manifest source path is unavailable")
	}
	root, err := openSourceParent(ctx, abs)
	if err != nil {
		return nil, err
	}
	name := filepath.Base(abs)
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		_ = root.Close()
		return nil, errors.New("manifest source must be an accessible regular file, not a symlink or directory")
	}
	if info.Size() > MaxSourceBytes {
		_ = root.Close()
		return nil, errors.New("manifest source exceeds the 1MiB limit")
	}
	file, err := root.Open(name)
	if err != nil {
		_ = root.Close()
		return nil, errors.New("manifest source cannot be opened")
	}
	handle := &sourceFile{file: file, root: root, info: info, path: abs, name: name}
	opened, statErr := file.Stat()
	if statErr != nil || !sameSourceFile(info, opened) {
		handle.close()
		return nil, errors.New("manifest source changed while opening")
	}
	return handle, nil
}

// Walk ancestor handles only to open the explicit file securely. Symlink
// ancestors are refused; no directory is enumerated for manifest input.
func openSourceParent(ctx context.Context, path string) (*os.Root, error) {
	anchor := filepath.VolumeName(path) + string(filepath.Separator)
	root, err := os.OpenRoot(anchor)
	if err != nil {
		return nil, errors.New("manifest source directory is unavailable")
	}
	parent := strings.TrimPrefix(filepath.Dir(path), anchor)
	for _, part := range strings.Split(parent, string(filepath.Separator)) {
		if contextErr := ctx.Err(); contextErr != nil {
			_ = root.Close()
			return nil, contextErr
		}
		if part == "" || part == "." {
			continue
		}
		info, statErr := root.Lstat(part)
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			_ = root.Close()
			return nil, errors.New("manifest source requires real directory ancestors, not symlinks")
		}
		next, openErr := root.OpenRoot(part)
		if openErr != nil {
			_ = root.Close()
			return nil, errors.New("manifest source directory cannot be opened")
		}
		opened, statErr := next.Stat(".")
		_ = root.Close()
		if statErr != nil || !os.SameFile(info, opened) {
			_ = next.Close()
			return nil, errors.New("manifest source directory changed while opening")
		}
		root = next
	}
	return root, nil
}

func (s *sourceFile) checkStable(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	opened, err := s.file.Stat()
	if err != nil || !sameSourceFile(s.info, opened) {
		return errors.New("manifest source changed while reading")
	}
	current, err := openSourceParent(ctx, s.path)
	if err != nil {
		return err
	}
	defer func() { _ = current.Close() }()
	info, err := current.Lstat(s.name)
	if err != nil || !sameSourceFile(s.info, info) {
		return errors.New("manifest source path changed while reading")
	}
	return nil
}

func sameSourceFile(before, after os.FileInfo) bool {
	return before != nil && after != nil && after.Mode().IsRegular() && os.SameFile(before, after) &&
		before.Size() == after.Size() && before.ModTime().Equal(after.ModTime()) && before.Mode() == after.Mode()
}

func readSourceBytes(ctx context.Context, file io.Reader) ([]byte, error) {
	var data []byte
	chunk := make([]byte, sourceReadChunk)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := file.Read(chunk)
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		if n > MaxSourceBytes-len(data) {
			return nil, errors.New("manifest source exceeds the 1MiB limit")
		}
		data = append(data, chunk[:n]...)
		if errors.Is(err, io.EOF) {
			return data, nil
		}
		if err != nil {
			return nil, errors.New("manifest source cannot be read")
		}
		if n == 0 {
			return nil, errors.New("manifest source read made no progress")
		}
	}
}

func decodeSource(ctx context.Context, data []byte) ([]Manifest, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var objects []Manifest
	seen := make(map[string]bool)
	for document := 1; ; document++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var node yaml.Node
		if err := decoder.Decode(&node); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, sourceDocumentError(document, "contains invalid YAML")
		}
		if document > maxSourceDocuments {
			return nil, errors.New("manifest source contains too many YAML documents")
		}
		if len(node.Content) == 1 && node.Content[0].Kind == yaml.ScalarNode &&
			node.Content[0].Tag == sourceNullTag && node.Content[0].Value == "" {
			continue
		}
		if len(objects) >= MaxManifests {
			return nil, errors.New("manifest source exceeds the 64-object limit")
		}
		manifest, key, err := decodeManifest(ctx, &node, document)
		if err != nil {
			return nil, err
		}
		if seen[key] {
			return nil, sourceDocumentError(document, "duplicates an API identity already declared in this source")
		}
		seen[key] = true
		objects = append(objects, manifest)
	}
	if len(objects) == 0 {
		return nil, errors.New("manifest source contains no named Kubernetes objects")
	}
	return objects, nil
}

func decodeManifest(ctx context.Context, node *yaml.Node, document int) (Manifest, string, error) {
	if node.Kind != yaml.DocumentNode || len(node.Content) != 1 || node.Content[0].Kind != yaml.MappingNode {
		return Manifest{}, "", sourceDocumentError(document, "must contain one Kubernetes object mapping")
	}
	nodes := 0
	value, err := sourceValue(ctx, node.Content[0], 0, &nodes)
	if err != nil {
		if ctx.Err() != nil {
			return Manifest{}, "", ctx.Err()
		}
		return Manifest{}, "", sourceDocumentError(document, err.Error())
	}
	object, ok := value.(map[string]any)
	if !ok {
		return Manifest{}, "", sourceDocumentError(document, "must contain a Kubernetes object mapping")
	}
	manifest, key, err := identifyManifest(object, document)
	if err != nil {
		return Manifest{}, "", err
	}
	manifest.Object = object
	if strings.EqualFold(manifest.Kind, sourceSecretKind) {
		metadata := map[string]any{sourceNameKey: manifest.Name}
		if manifest.Namespace != "" {
			metadata[sourceNamespaceKey] = manifest.Namespace
		}
		manifest.Object = map[string]any{"apiVersion": manifest.APIVersion, "kind": manifest.Kind, sourceMetadataKey: metadata}
		manifest.SecretExcluded = true
	}
	return manifest, key, nil
}

func identifyManifest(object map[string]any, document int) (Manifest, string, error) {
	manifest := Manifest{Document: document}
	manifest.APIVersion, _ = object["apiVersion"].(string)
	manifest.Kind, _ = object["kind"].(string)
	if !sourceIdentityText(manifest.APIVersion, 320) || !sourceKind(manifest.Kind) {
		return Manifest{}, "", sourceDocumentError(document, "requires string apiVersion and kind")
	}
	gv, err := schema.ParseGroupVersion(manifest.APIVersion)
	if err != nil || gv.String() != manifest.APIVersion || gv.Version == "" || len(validation.IsDNS1035Label(gv.Version)) != 0 ||
		(gv.Group != "" && len(validation.IsDNS1123Subdomain(gv.Group)) != 0) {
		return Manifest{}, "", sourceDocumentError(document, "contains an invalid apiVersion")
	}
	if strings.HasSuffix(manifest.Kind, "List") {
		return Manifest{}, "", sourceDocumentError(document, "uses an unsupported Kubernetes List; provide separate named manifests")
	}
	metadata, _ := object[sourceMetadataKey].(map[string]any)
	manifest.Name, _ = metadata[sourceNameKey].(string)
	if !sourceIdentityText(manifest.Name, 253) || len(pathvalidation.IsValidPathSegmentName(manifest.Name)) != 0 {
		return Manifest{}, "", sourceDocumentError(document, "requires a valid string metadata.name")
	}
	if namespace, exists := metadata[sourceNamespaceKey]; exists {
		manifest.Namespace, _ = namespace.(string)
		if manifest.Namespace == "" || len(validation.IsDNS1123Label(manifest.Namespace)) != 0 {
			return Manifest{}, "", sourceDocumentError(document, "requires a valid string metadata.namespace when declared")
		}
	}
	key := gv.Group + "/" + manifest.Kind + "/" + manifest.Namespace + "/" + manifest.Name
	return manifest, key, nil
}

func sourceValue(ctx context.Context, node *yaml.Node, depth int, nodes *int) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	*nodes++
	if depth > maxSourceDepth || *nodes > maxSourceNodes {
		return nil, errors.New("exceeds source field or nesting limits")
	}
	if node.Anchor != "" || node.Kind == yaml.AliasNode {
		return nil, errors.New("uses unsupported YAML anchors or aliases")
	}
	switch node.Kind {
	case yaml.MappingNode:
		return sourceMapping(ctx, node, depth, nodes)
	case yaml.SequenceNode:
		if node.Tag != sourceSequenceTag {
			return nil, errors.New("uses an unsupported YAML tag")
		}
		result := make([]any, 0, len(node.Content))
		for _, child := range node.Content {
			value, err := sourceValue(ctx, child, depth+1, nodes)
			if err != nil {
				return nil, err
			}
			result = append(result, value)
		}
		return result, nil
	case yaml.ScalarNode:
		return sourceScalar(node)
	default:
		return nil, errors.New("contains unsupported YAML structure")
	}
}

func sourceMapping(ctx context.Context, node *yaml.Node, depth int, nodes *int) (map[string]any, error) {
	if node.Tag != sourceMappingTag || len(node.Content)%2 != 0 {
		return nil, errors.New("uses an unsupported mapping tag or structure")
	}
	result := make(map[string]any, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i]
		if key.Kind != yaml.ScalarNode || key.Tag != sourceStringTag || key.Anchor != "" {
			return nil, errors.New("requires string mapping keys and does not support YAML merge keys")
		}
		if _, exists := result[key.Value]; exists {
			return nil, errors.New("contains duplicate mapping keys")
		}
		value, err := sourceValue(ctx, node.Content[i+1], depth+1, nodes)
		if err != nil {
			return nil, err
		}
		result[key.Value] = value
	}
	return result, nil
}

func sourceScalar(node *yaml.Node) (any, error) {
	switch node.Tag {
	case sourceStringTag, "!!timestamp":
		return node.Value, nil
	case sourceNullTag:
		return nil, nil
	case "!!bool":
		var value bool
		if err := node.Decode(&value); err == nil {
			return value, nil
		}
	case "!!int", "!!float":
		var value any
		if err := node.Decode(&value); err != nil {
			break
		}
		switch number := value.(type) {
		case int:
			return int64(number), nil
		case int64:
			return number, nil
		case uint64:
			return json.Number(strconv.FormatUint(number, 10)), nil
		case float64:
			if !math.IsInf(number, 0) && !math.IsNaN(number) {
				return number, nil
			}
		}
	}
	return nil, errors.New("contains a scalar or tag that is not JSON-compatible")
}

func sourcePrintable(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

func sourceIdentityText(value string, limit int) bool {
	return value != "" && len(value) <= limit && sourcePrintable(value) && strings.TrimSpace(value) == value
}

func sourceKind(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for i, character := range value {
		letter := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
		digit := character >= '0' && character <= '9'
		if !letter && (i == 0 || !digit) {
			return false
		}
	}
	return true
}

func sourceDocumentError(document int, message string) error {
	return fmt.Errorf("manifest document %d %s", document, message)
}
