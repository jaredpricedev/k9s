// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package dao

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/data"
	"github.com/derailed/k9s/internal/render/helm"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

var (
	_ Accessor  = (*HelmHistory)(nil)
	_ Nuker     = (*HelmHistory)(nil)
	_ Describer = (*HelmHistory)(nil)
	_ Valuer    = (*HelmHistory)(nil)
)

// HelmHistory represents a helm chart.
type HelmHistory struct {
	NonResource
}

// List returns a collection of resources.
func (h *HelmHistory) List(ctx context.Context, _ string) ([]runtime.Object, error) {
	path, ok := ctx.Value(internal.KeyFQN).(string)
	if !ok {
		return nil, fmt.Errorf("expecting FQN in context")
	}
	ns, n := client.Namespaced(path)

	cfg, err := ensureHelmConfig(h.Client().Config().Flags(), ns)
	if err != nil {
		return nil, err
	}

	hh, err := action.NewHistory(cfg).Run(n)
	if err != nil {
		return nil, err
	}

	oo := make([]runtime.Object, 0, len(hh))
	for _, r := range hh {
		oo = append(oo, helm.ReleaseRes{Release: r})
	}

	return oo, nil
}

// Get returns a resource.
func (h *HelmHistory) Get(ctx context.Context, path string) (runtime.Object, error) {
	fqn, rev, found := strings.Cut(path, ":")
	if !found || rev == "" {
		return nil, fmt.Errorf("invalid path %q", path)
	}

	ns, n := client.Namespaced(fqn)
	cfg, err := ensureHelmOperationConfig(ctx, h.Client().Config().Flags(), ns)
	if err != nil {
		return nil, err
	}

	getter := action.NewGet(cfg)
	getter.Version, err = strconv.Atoi(rev)
	if err != nil {
		return nil, err
	}

	resp, err := getter.Run(n)
	if err != nil {
		return nil, err
	}

	return helm.ReleaseRes{Release: resp}, nil
}

// Describe returns the chart notes.
func (h *HelmHistory) Describe(path string) (string, error) {
	rel, err := h.Get(context.Background(), path)
	if err != nil {
		return "", err
	}

	resp, ok := rel.(helm.ReleaseRes)
	if !ok {
		return "", fmt.Errorf("expected helm.ReleaseRes, but got %T", rel)
	}

	return resp.Release.Info.Notes, nil
}

// ToYAML returns the chart manifest.
func (h *HelmHistory) ToYAML(path string, _ bool) (string, error) {
	rel, err := h.Get(context.Background(), path)
	if err != nil {
		return "", err
	}

	resp, ok := rel.(helm.ReleaseRes)
	if !ok {
		return "", fmt.Errorf("expected helm.ReleaseRes, but got %T", rel)
	}

	return resp.Release.Manifest, nil
}

// GetValues return the config for this chart.
func (h *HelmHistory) GetValues(path string, allValues bool) ([]byte, error) {
	rel, err := h.Get(context.Background(), path)
	if err != nil {
		return nil, err
	}

	resp, ok := rel.(helm.ReleaseRes)
	if !ok {
		return nil, fmt.Errorf("expected helm.ReleaseRes, but got %T", rel)
	}

	var content any
	if allValues {
		content = resp.Release.Chart.Values
	} else {
		content = resp.Release.Config
	}

	return data.WriteYAML(content)
}

func (h *HelmHistory) Rollback(ctx context.Context, path, rev string) error {
	return h.RollbackSnapshot(ctx, path, rev, "")
}

// RollbackSnapshot revalidates the reviewed immutable release revision before
// invoking Helm. The fingerprint is provider identity, not a Kubernetes UID.
func (h *HelmHistory) RollbackSnapshot(ctx context.Context, path, rev, fingerprint string) error {
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	ns, n := client.Namespaced(path)
	cfg, err := ensureHelmOperationConfig(ctx, h.Client().Config().Flags(), ns)
	if err != nil {
		return err
	}

	ver, err := strconv.Atoi(rev)
	if err != nil {
		return fmt.Errorf("could not convert revision to a number: %w", err)
	}
	if ver <= 0 {
		return fmt.Errorf("select an explicit positive Helm revision")
	}
	current, err := cfg.Releases.Last(n)
	if err != nil {
		return err
	}
	if current.Info == nil || current.Info.Status.IsPending() {
		return fmt.Errorf("Helm release is pending another operation; review its status before rollback")
	}
	if fingerprint != "" {
		target, err := cfg.Releases.Get(n, ver)
		if err != nil {
			return err
		}
		actual, err := HelmRevisionFingerprint(target)
		if err != nil {
			return err
		}
		if actual != fingerprint {
			return fmt.Errorf("selected Helm revision changed; reopen the rollback review")
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	clt := action.NewRollback(cfg)
	clt.Version = ver
	if deadline, ok := ctx.Deadline(); ok {
		clt.Timeout = max(time.Nanosecond, time.Until(deadline))
	}

	return clt.Run(n)
}

// HelmRevisionFingerprint retains identity without retaining Secret/config
// values in an operation receipt. Helm itself owns applying the revision.
func HelmRevisionFingerprint(revision *release.Release) (string, error) {
	if revision == nil || revision.Name == "" || revision.Namespace == "" || revision.Version <= 0 {
		return "", fmt.Errorf("Helm revision identity is unavailable")
	}
	encoded, err := json.Marshal(revision)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(encoded)), nil
}

// Delete uninstall a Helm.
func (h *HelmHistory) Delete(ctx context.Context, path string, _ *metav1.DeletionPropagation, _ Grace) error {
	return uninstallHelm(ctx, h.Client().Config().Flags(), path, false)
}
