// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package dao

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/data"
	"github.com/derailed/k9s/internal/render/helm"
	"github.com/derailed/k9s/internal/slogs"
	"helm.sh/helm/v3/pkg/action"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"
)

var (
	_ Accessor  = (*HelmChart)(nil)
	_ Nuker     = (*HelmChart)(nil)
	_ Describer = (*HelmChart)(nil)
	_ Valuer    = (*HelmChart)(nil)
)

// HelmChart represents a helm chart.
type HelmChart struct {
	NonResource
}

// List returns a collection of resources.
func (h *HelmChart) List(_ context.Context, ns string) ([]runtime.Object, error) {
	cfg, err := ensureHelmConfig(h.Client().Config().Flags(), ns)
	if err != nil {
		return nil, err
	}

	list := action.NewList(cfg)
	list.All = true
	list.SetStateMask()
	rr, err := list.Run()
	if err != nil {
		return nil, err
	}

	oo := make([]runtime.Object, 0, len(rr))
	for _, r := range rr {
		oo = append(oo, helm.ReleaseRes{Release: r})
	}

	return oo, nil
}

// Get returns a resource.
func (h *HelmChart) Get(_ context.Context, path string) (runtime.Object, error) {
	ns, n := client.Namespaced(path)
	cfg, err := ensureHelmConfig(h.Client().Config().Flags(), ns)
	if err != nil {
		return nil, err
	}
	resp, err := action.NewGet(cfg).Run(n)
	if err != nil {
		return nil, err
	}

	return helm.ReleaseRes{Release: resp}, nil
}

// GetValues returns values for a release
func (h *HelmChart) GetValues(path string, allValues bool) ([]byte, error) {
	ns, n := client.Namespaced(path)
	cfg, err := ensureHelmConfig(h.Client().Config().Flags(), ns)
	if err != nil {
		return nil, err
	}
	vals := action.NewGetValues(cfg)
	vals.AllValues = allValues
	resp, err := vals.Run(n)
	if err != nil {
		return nil, err
	}

	return data.WriteYAML(resp)
}

// Describe returns the chart notes.
func (h *HelmChart) Describe(path string) (string, error) {
	ns, n := client.Namespaced(path)
	cfg, err := ensureHelmConfig(h.Client().Config().Flags(), ns)
	if err != nil {
		return "", err
	}
	resp, err := action.NewGet(cfg).Run(n)
	if err != nil {
		return "", err
	}

	return resp.Info.Notes, nil
}

// ToYAML returns the chart manifest.
func (h *HelmChart) ToYAML(path string, _ bool) (string, error) {
	ns, n := client.Namespaced(path)
	cfg, err := ensureHelmConfig(h.Client().Config().Flags(), ns)
	if err != nil {
		return "", err
	}
	resp, err := action.NewGet(cfg).Run(n)
	if err != nil {
		return "", err
	}

	return resp.Manifest, nil
}

// Delete uninstall a HelmChart.
func (h *HelmChart) Delete(ctx context.Context, path string, _ *metav1.DeletionPropagation, _ Grace) error {
	return uninstallHelm(ctx, h.Client().Config().Flags(), path, false)
}

// Uninstall uninstalls a HelmChart.
func (h *HelmChart) Uninstall(path string, keepHist bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), h.Client().Config().CallTimeout())
	defer cancel()
	return uninstallHelm(ctx, h.Client().Config().Flags(), path, keepHist)
}

// Helm's uninstall API does not accept a context. Bind every request to the
// operation deadline through the transport, including storage and hook clients.
func uninstallHelm(ctx context.Context, flags *genericclioptions.ConfigFlags, path string, keepHist bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ns, n := client.Namespaced(path)
	pinned := client.SnapshotConfigFlags(flags)
	previous := pinned.WrapConfigFn
	pinned.WrapConfigFn = func(cfg *rest.Config) *rest.Config {
		if previous != nil {
			cfg = previous(cfg)
		}
		cfg = rest.CopyConfig(cfg)
		if deadline, ok := ctx.Deadline(); ok {
			cfg.Timeout = time.Until(deadline)
		}
		cfg.Wrap(func(base http.RoundTripper) http.RoundTripper { return helmOperationTransport{ctx: ctx, base: base} })
		return cfg
	}
	cfg, err := ensureHelmConfig(pinned, ns)
	if err != nil {
		return err
	}
	u := action.NewUninstall(cfg)
	u.KeepHistory = keepHist
	if deadline, ok := ctx.Deadline(); ok {
		u.Timeout = time.Until(deadline)
	}
	res, err := u.Run(n)
	if err != nil {
		return err
	}
	if res != nil && res.Info != "" {
		return fmt.Errorf("%s", res.Info)
	}
	return nil
}

type helmOperationTransport struct {
	ctx  context.Context
	base http.RoundTripper
}

func (t helmOperationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.ctx.Err(); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(req.Clone(t.ctx))
}

// ensureHelmConfig return a new configuration.
func ensureHelmConfig(flags *genericclioptions.ConfigFlags, ns string) (*action.Configuration, error) {
	settings := client.SnapshotConfigFlags(flags)
	settings.Namespace = &ns
	cfg := new(action.Configuration)
	err := cfg.Init(settings, ns, os.Getenv("HELM_DRIVER"), helmLogger)

	return cfg, err
}

func helmLogger(fmat string, args ...any) {
	slog.Debug("Log",
		slogs.Log, fmt.Sprintf(fmat, args...),
		slogs.Subsys, "helm",
	)
}
