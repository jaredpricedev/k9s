// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package review

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/logstream"
	"github.com/derailed/k9s/internal/provider"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	sourceProviderFile      = "file"
	sourceParentPath        = ".."
	sourceProviderGit       = "git"
	sourceProviderHelm      = "helm"
	sourceProviderKustomize = "kustomize"
)

const RenderTimeout = 30 * time.Second
const maxRenderFiles, maxRenderInputBytes = 512, 16 << 20

type SourceSpec struct {
	Name      string   `yaml:"name" json:"name"`
	Provider  string   `yaml:"provider" json:"provider"`
	Path      string   `yaml:"path" json:"path"`
	Root      string   `yaml:"root,omitempty" json:"root,omitempty"`
	Revision  string   `yaml:"revision,omitempty" json:"revision,omitempty"`
	Manifest  string   `yaml:"manifest,omitempty" json:"manifest,omitempty"`
	Release   string   `yaml:"release,omitempty" json:"release,omitempty"`
	Namespace string   `yaml:"namespace,omitempty" json:"namespace,omitempty"`
	Values    []string `yaml:"values,omitempty" json:"values,omitempty"`
}
type ProviderRun func(context.Context, provider.Input) provider.Result

type sourceExecution struct {
	output     []byte
	executable string
	args       []string
}

// LoadSourceProfile is invoked only after the operator explicitly selects an
// @profile. It does not install tools, fetch repositories, check out revisions,
// enable renderer plugins, or infer an authoritative pruning scope.
//
//nolint:gocritic // Provider contracts capture scope values before asynchronous execution.
func LoadSourceProfile(ctx context.Context, path string, scope provider.Scope, run ProviderRun) (Source, error) {
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
	spec, err := decodeSourceSpec(ctx, data)
	if err != nil {
		return Source{}, err
	}
	source, err := loadConfiguredSource(ctx, spec, filepath.Dir(handle.path), scope, run)
	if err != nil {
		return Source{}, err
	}
	if stableErr := handle.checkStable(ctx); stableErr != nil {
		return Source{}, stableErr
	}
	source.Identity.Path = handle.path
	source.Identity.Name = spec.Name
	source.Identity.Provider = spec.Provider
	digest := sha256.Sum256(data)
	source.Identity.ProfileSHA256 = hex.EncodeToString(digest[:])
	return source, nil
}
func decodeSourceSpec(ctx context.Context, data []byte) (SourceSpec, error) {
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil || len(node.Content) != 1 {
		return SourceSpec{}, errors.New("source profile contains invalid YAML")
	}
	nodes := 0
	if _, err := sourceValue(ctx, node.Content[0], 0, &nodes); err != nil {
		return SourceSpec{}, errors.New("source profile contains unsupported or excessive fields")
	}
	var spec SourceSpec
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&spec); err != nil {
		return SourceSpec{}, errors.New("source profile fields are invalid or unknown")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return SourceSpec{}, errors.New("source profile must contain one document")
	}
	if !sourceIdentityText(spec.Name, 128) || !sourceIdentityText(spec.Path, 4096) || len(spec.Values) > 8 {
		return SourceSpec{}, errors.New("source profile requires a bounded name, path and at most eight value files")
	}
	switch spec.Provider {
	case sourceProviderFile, sourceProviderKustomize, sourceProviderHelm, sourceProviderGit:
	default:
		return SourceSpec{}, errors.New("source profile provider must be file, kustomize, helm or git")
	}
	if spec.Provider != sourceProviderHelm && (spec.Release != "" || spec.Namespace != "" || len(spec.Values) > 0) {
		return SourceSpec{}, errors.New("Helm options require the helm provider")
	}
	if spec.Provider != sourceProviderGit && (spec.Revision != "" || spec.Manifest != "") {
		return SourceSpec{}, errors.New("revision and manifest require the git provider")
	}
	if spec.Root != "" && (spec.Provider != sourceProviderKustomize || !sourceIdentityText(spec.Root, 4096)) {
		return SourceSpec{}, errors.New("an explicit input root is supported only for Kustomize")
	}
	return spec, nil
}
func configuredPath(base, path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", errors.New("configured source path is invalid")
	}
	if strings.Contains(path, "://") {
		return "", errors.New("source providers require explicitly configured local paths")
	}
	return abs, nil
}

//nolint:gocritic // The immutable scope is retained across the renderer's separate version/render requests.
func loadConfiguredSource(ctx context.Context, spec SourceSpec, base string, scope provider.Scope, run ProviderRun) (Source, error) {
	path, err := configuredPath(base, spec.Path)
	if err != nil {
		return Source{}, err
	}
	if spec.Provider == sourceProviderFile {
		source, loadErr := LoadSource(ctx, path)
		if loadErr == nil {
			source.Identity.InputPath = path
			source.Identity.InputSHA256 = source.Identity.SHA256
		}
		return source, loadErr
	}
	if run == nil {
		run = provider.Run
	}
	ctx, cancel := context.WithTimeout(ctx, RenderTimeout)
	defer cancel()
	dir, err := openConfiguredDirectory(ctx, path)
	if err != nil {
		return Source{}, err
	}
	defer dir.Close()
	inputRoot, rootErr := configuredInputRoot(ctx, &spec, base, path)
	if rootErr != nil {
		return Source{}, rootErr
	}
	plan, err := configureRendererPlan(ctx, &spec, base, path, inputRoot)
	if err != nil {
		return Source{}, err
	}
	version, err := sourceCommand(ctx, run, scope, spec.Provider, path, plan.versionArgs, 4096)
	if err != nil {
		return Source{}, err
	}
	if strings.TrimSpace(logstream.SafeText(string(version.output))) == "" {
		return Source{}, errors.New("source executable returned no usable version; source was not rendered")
	}
	revision := ""
	if spec.Provider == sourceProviderGit {
		resolveArgs := []string{"rev-parse", "--verify", "--end-of-options", spec.Revision + "^{commit}"}
		resolved, resolveErr := sourceCommand(ctx, run, scope, sourceProviderGit, path, resolveArgs, 4096)
		if resolveErr != nil {
			return Source{}, resolveErr
		}
		revision = strings.TrimSpace(string(resolved.output))
		if !regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`).MatchString(revision) {
			return Source{}, errors.New("Git did not return an exact commit identity")
		}
		plan.args = []string{"show", revision + ":" + filepath.ToSlash(spec.Manifest)}
		plan.inputHash = revision
	}
	rendered, err := sourceCommand(ctx, run, scope, spec.Provider, path, plan.args, MaxSourceBytes)
	if err != nil {
		return Source{}, err
	}
	if spec.Provider == sourceProviderKustomize || spec.Provider == sourceProviderHelm {
		current, checkErr := renderInputHash(ctx, inputRoot, spec.Provider == sourceProviderKustomize)
		if checkErr != nil {
			return Source{}, checkErr
		}
		// Directory and separate value files must still match the inputs captured
		// before the renderer ran; the output hash alone cannot establish that.
		if current != plan.directoryHash {
			return Source{}, errors.New("renderer inputs changed while observing source")
		}
	}
	if valuesErr := checkValueInputs(ctx, plan.valueHashes); valuesErr != nil {
		return Source{}, valuesErr
	}
	if version.executable != rendered.executable {
		return Source{}, errors.New("source executable identity changed while rendering")
	}
	output := rendered.output
	objects, err := decodeSource(ctx, output)
	if err != nil {
		return Source{}, err
	}
	digest := sha256.Sum256(output)
	identity := SourceIdentity{
		Path: path, InputPath: inputRoot, Provider: spec.Provider, Name: spec.Name, Revision: revision, RequestedRevision: spec.Revision,
		InputSHA256: plan.inputHash, Renderer: rendered.executable,
		RendererVersion: logstream.SafeText(strings.TrimSpace(string(version.output))), Options: append([]string(nil), rendered.args...),
		SHA256: hex.EncodeToString(digest[:]), Bytes: int64(len(output)), Documents: len(objects), LoadedAt: time.Now().UTC(),
	}
	return Source{Identity: identity, Objects: objects}, nil
}

//nolint:gocritic // Provider scope values remain captured independently of workspace changes.
func sourceCommand(ctx context.Context, run ProviderRun, scope provider.Scope, kind, dir string, args []string, limit int64) (sourceExecution, error) {
	if err := ctx.Err(); err != nil {
		return sourceExecution{}, err
	}
	input := provider.Input{
		ProviderID: "review." + kind, Executable: kind, Dir: dir, Args: append([]string(nil), args...), Scope: scope,
		Limits: provider.Limits{Timeout: RenderTimeout, StdoutBytes: limit, StderrBytes: 8192},
	}
	if kind == sourceProviderGit {
		input.Args = append([]string{"--no-pager", "-c", "core.fsmonitor=false"}, input.Args...)
		input.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1")
	}
	result := run(ctx, input)
	if ctx.Err() != nil {
		return sourceExecution{}, ctx.Err()
	}
	if result.State != provider.Succeeded || result.Err != nil || result.Truncated {
		reason := "source command failed; renderer diagnostics withheld because they can contain confidential values"
		switch {
		case errors.Is(result.Err, provider.ErrAbsent):
			reason = "executable is absent; no tool was installed"
		case errors.Is(result.Err, provider.ErrDenied):
			reason = "executable permission denied"
		case result.Truncated || result.State == provider.OutputLimit:
			reason = "command output exceeded its limit"
		case result.State == provider.TimedOut:
			reason = "source command timed out"
		case result.State == provider.Canceled:
			reason = "source command canceled"
		}
		return sourceExecution{}, fmt.Errorf("%s %s; prior source remains available", kind, reason)
	}
	if result.Scope != scope {
		return sourceExecution{}, errors.New("source provider returned a different captured scope")
	}
	if int64(len(result.Stdout)) > limit {
		return sourceExecution{}, errors.New("source command output exceeded its limit")
	}
	if result.Source == "" {
		return sourceExecution{}, errors.New("source provider did not retain the resolved executable identity")
	}
	return sourceExecution{output: result.Stdout, executable: result.Source, args: input.Args}, nil
}
func openConfiguredDirectory(ctx context.Context, path string) (*os.Root, error) {
	root, err := openSourceParent(ctx, filepath.Join(path, "source-directory-check"))
	if err != nil {
		return nil, err
	}
	info, err := root.Stat(".")
	if err != nil || !info.IsDir() {
		root.Close()
		return nil, errors.New("source directory is unavailable")
	}
	return root, nil
}
func validGitManifest(path string) bool {
	return sourceIdentityText(path, 4096) && !filepath.IsAbs(path) && filepath.Clean(path) == path &&
		path != "." && !strings.Contains(path, "\\") && !strings.Contains(path, ":") && path != sourceParentPath && !strings.HasPrefix(path, "../")
}
func combineRenderDigest(prior, path, digest string) string {
	sum := sha256.Sum256([]byte(prior + "\x00" + path + "\x00" + digest))
	return hex.EncodeToString(sum[:])
}

func renderInputHash(ctx context.Context, path string, kustomize bool) (string, error) {
	var files []string
	total := int64(0)
	err := filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return errors.New("renderer input cannot be read")
		}
		rel, err := filepath.Rel(path, current)
		if err != nil {
			return errors.New("renderer input path is invalid")
		}
		if strings.Count(filepath.ToSlash(rel), "/") > 16 {
			return errors.New("renderer directory exceeds depth limit")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("renderer inputs must not contain symlinks")
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("renderer inputs must be regular files")
		}
		info, err := entry.Info()
		if err != nil {
			return errors.New("renderer input is unavailable")
		}
		total += info.Size()
		if total > maxRenderInputBytes || len(files) >= maxRenderFiles {
			return errors.New("renderer inputs exceed bounded file/byte limits")
		}
		files = append(files, current)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	hash := ""
	for _, file := range files {
		handle, err := openSourceFile(ctx, file)
		if err != nil {
			return "", err
		}
		data, err := readSourceBytes(ctx, handle.file)
		stableErr := handle.checkStable(ctx)
		handle.close()
		if err != nil {
			return "", err
		}
		if stableErr != nil {
			return "", stableErr
		}
		rel, _ := filepath.Rel(path, file)
		if kustomize && isKustomization(filepath.Base(file)) {
			if err := validateKustomization(ctx, data, path, filepath.Dir(file)); err != nil {
				return "", err
			}
		}
		sum := sha256.Sum256(data)
		hash = combineRenderDigest(hash, filepath.ToSlash(rel), hex.EncodeToString(sum[:]))
	}
	return hash, nil
}
func isKustomization(name string) bool {
	return name == "kustomization.yaml" || name == "kustomization.yml" || name == "Kustomization"
}
func validateKustomization(ctx context.Context, data []byte, root, dir string) error {
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil || len(node.Content) != 1 {
		return errors.New("Kustomize configuration is invalid")
	}
	count := 0
	value, err := sourceValue(ctx, node.Content[0], 0, &count)
	if err != nil {
		return errors.New("Kustomize configuration is unsupported")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return errors.New("Kustomize configuration must be a mapping")
	}
	for _, field := range []string{"generators", "transformers", "helmCharts", "helmGlobals"} {
		if _, exists := object[field]; exists {
			return errors.New("external Kustomize generators, transformers and Helm rendering are excluded")
		}
	}
	var check func(any) error
	check = func(value any) error {
		switch value := value.(type) {
		case string:
			if remoteKustomizeReference(value) {
				return errors.New("Kustomize references must be explicit local inputs; implicit network references are excluded")
			}
			// References can include ConfigMap generator key=path syntax.
			if i := strings.IndexByte(value, '='); i >= 0 {
				value = value[i+1:]
			}
			if remoteKustomizeReference(value) || filepath.IsAbs(value) {
				return errors.New("Kustomize references must stay inside the configured local directory")
			}
			resolved := filepath.Clean(filepath.Join(dir, value))
			rel, err := filepath.Rel(root, resolved)
			if err != nil || rel == sourceParentPath || strings.HasPrefix(rel, sourceParentPath+string(filepath.Separator)) {
				return errors.New("Kustomize reference leaves the configured local directory")
			}
			for _, part := range strings.Split(rel, string(filepath.Separator)) {
				if part == ".git" {
					return errors.New("Kustomize references must not read excluded repository metadata")
				}
			}
			if _, err := os.Lstat(resolved); err != nil {
				return errors.New("Kustomize reference is not an available local input")
			}
		case []any:
			for _, item := range value {
				if err := check(item); err != nil {
					return err
				}
			}
		case map[string]any:
			for key, item := range value {
				if key == "path" || key == "files" || key == "envs" || key == "env" || key == "openapi" || key == "configurations" {
					if err := check(item); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	for _, field := range []string{
		"resources", "bases", "components", "patchesStrategicMerge", "patches", "patchesJson6902",
		"configMapGenerator", "secretGenerator", "configurations", "openapi", "crds", "replacements",
	} {
		if err := check(object[field]); err != nil {
			return err
		}
	}
	return nil
}

func remoteKustomizeReference(value string) bool {
	if strings.ContainsAny(value, ":@?") {
		return true
	}
	first, _, slash := strings.Cut(value, "/")
	// Host-like references can trigger Git even if a same-named local directory
	// exists. Dot-named local directories remain expressible with a ./ prefix.
	return slash && first != "." && first != sourceParentPath && strings.Contains(first, ".") && !strings.Contains(first, "=")
}

func checkValueInputs(ctx context.Context, valueHashes map[string]string) error {
	for valuePath, before := range valueHashes {
		h, err := openSourceFile(ctx, valuePath)
		if err != nil {
			return err
		}
		data, err := readSourceBytes(ctx, h.file)
		stable := h.checkStable(ctx)
		h.close()
		if err != nil {
			return err
		}
		if stable != nil {
			return stable
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != before {
			return errors.New("Helm values changed while rendering")
		}
	}
	return nil
}

type rendererPlan struct {
	inputHash, directoryHash string
	valueHashes              map[string]string
	args, versionArgs        []string
}

func configureRendererPlan(ctx context.Context, spec *SourceSpec, base, path, inputRoot string) (rendererPlan, error) {
	var err error
	inputHash := ""
	directoryHash := ""
	valueHashes := make(map[string]string)
	var args, versionArgs []string
	switch spec.Provider {
	case sourceProviderKustomize:
		inputHash, err = renderInputHash(ctx, inputRoot, true)
		if err != nil {
			return rendererPlan{}, err
		}
		directoryHash = inputHash
		versionArgs = []string{"version"}
		args = []string{"build", path, "--load-restrictor", "LoadRestrictionsRootOnly"}
	case sourceProviderHelm:
		if len(validation.IsDNS1123Subdomain(spec.Release)) != 0 || spec.Release == "" || !validNamespace(spec.Namespace) {
			return rendererPlan{}, errors.New("Helm source requires an explicit release name and namespace")
		}
		inputHash, err = renderInputHash(ctx, path, false)
		if err != nil {
			return rendererPlan{}, err
		}
		directoryHash = inputHash
		versionArgs = []string{"version", "--short"}
		args = []string{"template", spec.Release, path, "--namespace", spec.Namespace}
		for _, value := range spec.Values {
			valuePath, pathErr := configuredPath(base, value)
			if pathErr != nil {
				return rendererPlan{}, pathErr
			}
			h, openErr := openSourceFile(ctx, valuePath)
			if openErr != nil {
				return rendererPlan{}, openErr
			}
			valueBytes, readErr := readSourceBytes(ctx, h.file)
			stableErr := h.checkStable(ctx)
			h.close()
			if readErr != nil {
				return rendererPlan{}, readErr
			}
			if stableErr != nil {
				return rendererPlan{}, stableErr
			}
			digest := sha256.Sum256(valueBytes)
			valueHashes[valuePath] = hex.EncodeToString(digest[:])
			inputHash = combineRenderDigest(inputHash, valuePath, hex.EncodeToString(digest[:]))
			args = append(args, "--values", valuePath)
		}
	case sourceProviderGit:
		if !sourceIdentityText(spec.Revision, 256) || !validGitManifest(spec.Manifest) {
			return rendererPlan{}, errors.New("Git source requires an explicit revision and repository-relative manifest path")
		}
		versionArgs = []string{"--version"}
	}
	return rendererPlan{inputHash: inputHash, directoryHash: directoryHash, valueHashes: valueHashes, args: args, versionArgs: versionArgs}, nil
}

func configuredInputRoot(ctx context.Context, spec *SourceSpec, base, path string) (string, error) {
	if spec.Root == "" {
		return path, nil
	}
	root, err := configuredPath(base, spec.Root)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == sourceParentPath || strings.HasPrefix(relative, sourceParentPath+string(filepath.Separator)) {
		return "", errors.New("Kustomize path must stay within its explicit local input root")
	}
	handle, err := openConfiguredDirectory(ctx, root)
	if err != nil {
		return "", err
	}
	handle.Close()
	return root, nil
}
