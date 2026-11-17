// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import "github.com/derailed/k9s/internal/ui"

// runnableNextChecks keeps the next-step checks that the action registry
// reports as available for the selected row in this view, so the line never
// prints a key that does nothing or a command that would be refused. The
// registry resolves the selection from the informer cache; nothing here
// reads the cluster.
func (b *Browser) runnableNextChecks(checks []ui.NextCheck) []ui.NextCheck {
	if b.app == nil || b.app.Config == nil || len(checks) == 0 {
		return nil
	}
	catalog := actionCatalog(b, b.app)
	runnable := make([]ui.NextCheck, 0, len(checks))
	for _, check := range checks {
		for i := range catalog {
			item := &catalog[i]
			if item.Shortcut == check.Shortcut && (check.Key == 0 || item.Key == check.Key) && item.Available() {
				runnable = append(runnable, check)
				break
			}
		}
	}
	return runnable
}
