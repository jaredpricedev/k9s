// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/render/helm"
	"github.com/derailed/k9s/internal/ui/dialog"
	"k8s.io/client-go/rest"
)

// Resolve before provider review, then preserve the same transport through the
// later confirmation and execution even if the kubeconfig file is edited.
func freezeHelmOperationDestination(factory dao.Factory) error {
	if factory == nil || factory.Client() == nil || factory.Client().Config() == nil {
		return fmt.Errorf("Helm operation destination is unavailable")
	}
	flags := factory.Client().Config().Flags()
	destination, err := factory.Client().RestConfig()
	if err != nil {
		return err
	}
	if destination == nil {
		return fmt.Errorf("Helm operation destination is unavailable")
	}
	flags.WrapConfigFn = func(*rest.Config) *rest.Config { return rest.CopyConfig(destination) }
	return nil
}

func (b *Browser) guardedHelmDelete(selections []string) {
	if len(selections) == 0 || len(selections) > maxOperationTargets {
		b.app.Flash().Warnf("Select 1 to %d Helm releases", maxOperationTargets)
		return
	}
	session, err := captureOperationScreen(b)
	if err != nil {
		b.app.Flash().Err(err)
		return
	}
	factory, err := captureSyntheticOperationFactory(b, session.context)
	if err != nil {
		b.app.Flash().Err(err)
		return
	}
	var chart dao.HelmChart
	chart.Init(factory, b.GVR())
	paths := slices.Clone(selections)
	b.app.Flash().Info("Reading Helm release identities before confirmation...")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), boundedOperationTimeout(session.timeout))
		defer cancel()
		if destinationErr := freezeHelmOperationDestination(factory); destinationErr != nil {
			session.dispatch(func() { b.app.Flash().Err(destinationErr) })
			return
		}
		targets := make([]SelectedResourceTarget, 0, len(paths))
		fingerprints := make(map[string]string, len(paths))
		var identities strings.Builder
		for _, path := range paths {
			target := SelectedResourceTarget{Context: session.context, GVR: b.GVR(), Name: path}
			obj, err := chart.Get(ctx, path)
			if err == nil {
				revision, ok := obj.(helm.ReleaseRes)
				if !ok {
					err = fmt.Errorf("Helm release identity is unavailable")
				} else {
					fingerprints[path], err = dao.HelmRevisionFingerprint(revision.Release)
				}
				if err == nil {
					fmt.Fprintf(&identities, "%s revision %d · %s\n", path, revision.Release.Version, fingerprints[path])
				}
			}
			if err != nil {
				target.UnavailableReason = "Helm review failed: " + operationError(err)
				fmt.Fprintf(&identities, "%s: unavailable, will not be submitted\n", path)
			}
			targets = append(targets, target)
		}
		session.dispatch(func() {
			msg := fmt.Sprintf("Uninstall %d Helm release(s)?\nContext: %s\n%s\nHelm retains ownership. Provider fingerprints are "+
				"rechecked, without an atomic release lock. Cancellation does not undo accepted writes.", len(targets), session.context, identities.String())
			d := b.app.Styles.Dialog()
			dialog.ShowConfirm(&d, b.app.Content.Pages, "Confirm Helm uninstall", msg, func() {
				session.submit("Helm uninstall", targets, func(ctx context.Context, target SelectedResourceTarget) error {
					fingerprint := fingerprints[target.Path()]
					fmt.Fprintf(operationOutput(ctx), "Provider identity: %s\nNo atomic release lock is supplied; Helm owns the uninstall.\n", fingerprint)
					return chart.DeleteSnapshot(observeHelmOperation(ctx), target.Path(), fingerprint)
				}, func(target SelectedResourceTarget) { b.GetTable().DeleteMark(target.Path()) })
			}, func() {})
		})
	}()
}
