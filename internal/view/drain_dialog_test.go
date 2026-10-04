// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDrainNumericOptions(t *testing.T) {
	for _, value := range []string{"-1", "0", "90", " 90 ", strconv.Itoa(int(^uint(0) >> 1))} {
		t.Run("grace/"+value, func(t *testing.T) {
			_, err := asIntOpt(value)
			require.NoError(t, err)
		})
	}
	for _, value := range []string{"", " ", "oops", "-2", "1.5", "999999999999999999999999999"} {
		t.Run("invalid-grace/"+value, func(t *testing.T) {
			_, err := asIntOpt(value)
			require.ErrorContains(t, err, "GracePeriod:")
		})
	}
	for _, value := range []string{"0", "0s", "5s", "2m", "1m30s", " 90s ", "2562047h47m16.854775807s"} {
		t.Run("timeout/"+value, func(t *testing.T) {
			_, err := asDurOpt(value)
			require.NoError(t, err)
		})
	}
	for _, value := range []string{"", " ", "oops", "90", "-1s", "999999999999999999999999999s"} {
		t.Run("invalid-timeout/"+value, func(t *testing.T) {
			_, err := asDurOpt(value)
			require.ErrorContains(t, err, "Timeout:")
		})
	}
}

func TestDrainRejectsVisibleValidToInvalidEditsAndAllowsCorrection(t *testing.T) {
	for _, tc := range []struct {
		label, valid, invalid, corrected string
		index                            int
	}{
		{"GracePeriod:", "90", "oops", "15", 0},
		{"Timeout:", "90s", "oops", "2m", 1},
		{"GracePeriod:", "90", "", "0", 0},
		{"Timeout:", "90s", "", "0", 1},
	} {
		t.Run(tc.label+tc.invalid, func(t *testing.T) {
			var called int
			var got dao.DrainOptions
			app, form, modal := newDrainFormTest(t, []string{"fixture-node"}, func(_ ResourceViewer, _ []string, opts dao.DrainOptions) {
				called++
				got = opts
			})
			field := form.GetFormItem(tc.index).(*tview.InputField)
			replaceDrainField(t, app, form, tc.index, tc.valid)
			replaceDrainField(t, app, form, tc.index, tc.invalid)
			assert.Equal(t, tc.invalid, field.GetText())
			assert.NotContains(t, drainDialogText(t, modal), "enter")
			pressDrainButton(t, app, form, "OK")
			require.Zero(t, called, "invalid visible value must never invoke maintenance")
			require.Same(t, modal, app.Content.GetPrimitive(drainKey))
			assert.Same(t, field, app.GetFocus(), "first invalid field receives focus")
			assert.Contains(t, drainDialogText(t, modal), tc.label+" enter")
			assert.Contains(t, drainDialogText(t, modal), "fixture-node")
			// The field-specific message persists through incomplete edits. Editing
			// does not flash, dismiss the dialog or repeatedly change focus.
			replaceDrainField(t, app, form, tc.index, tc.corrected)
			assert.Contains(t, drainDialogText(t, modal), tc.label+" enter")
			pressDrainButton(t, app, form, "OK")
			require.Equal(t, 1, called)
			assert.Nil(t, app.Content.GetPrimitive(drainKey))
			if tc.index == 0 {
				want, err := strconv.Atoi(tc.corrected)
				require.NoError(t, err)
				assert.Equal(t, want, got.GracePeriodSeconds)
				assert.Equal(t, 5*time.Second, got.Timeout)
			} else {
				want, err := time.ParseDuration(tc.corrected)
				require.NoError(t, err)
				assert.Equal(t, want, got.Timeout)
				assert.Equal(t, -1, got.GracePeriodSeconds)
			}
		})
	}
}

func TestDrainReadsAllVisibleOptionsTogether(t *testing.T) {
	var got dao.DrainOptions
	app, form, modal := newDrainFormTest(t, []string{"fixture-node"}, func(_ ResourceViewer, _ []string, opts dao.DrainOptions) {
		got = opts
	})
	form.GetFormItem(0).(*tview.InputField).SetText("oops")
	form.GetFormItem(1).(*tview.InputField).SetText("-1s")
	pressDrainButton(t, app, form, "OK")
	assert.Contains(t, drainDialogText(t, modal), "GracePeriod: enter")
	assert.Contains(t, drainDialogText(t, modal), "Timeout: enter")
	assert.Same(t, form.GetFormItem(0), app.GetFocus())
	form.GetFormItem(0).(*tview.InputField).SetText("-1")
	form.GetFormItem(1).(*tview.InputField).SetText("0")
	for i := 2; i < form.GetFormItemCount(); i++ {
		form.SetFocus(i)
		app.SetFocus(form)
		pressDrainKey(t, app, tcell.NewEventKey(tcell.KeyRune, ' ', tcell.ModNone))
		assert.True(t, form.GetFormItem(i).(*tview.Checkbox).IsChecked())
	}
	pressDrainButton(t, app, form, "OK")
	assert.Equal(t, dao.DrainOptions{
		GracePeriodSeconds: -1, Timeout: 0, IgnoreAllDaemonSets: true,
		DeleteEmptyDirData: true, Force: true, DisableEviction: true,
	}, got)
}

func TestDrainCancellationNeverSubmits(t *testing.T) {
	for _, cancel := range []string{"Cancel", "Escape"} {
		t.Run(cancel, func(t *testing.T) {
			called := 0
			app, form, _ := newDrainFormTest(t, []string{"fixture-node"}, func(ResourceViewer, []string, dao.DrainOptions) { called++ })
			replaceDrainField(t, app, form, 0, "oops")
			pressDrainButton(t, app, form, "OK")
			if cancel == "Cancel" {
				pressDrainButton(t, app, form, "Cancel")
			} else {
				pressDrainKey(t, app, tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
			}
			assert.Nil(t, app.Content.GetPrimitive(drainKey))
			assert.Zero(t, called)
			// Even a retained widget callback cannot submit after cancellation.
			form.GetFormItem(0).(*tview.InputField).SetText("90")
			pressDrainButton(t, app, form, "OK")
			assert.Zero(t, called)
		})
	}
}

func TestDrainRetainsSelectionAndRechecksReadOnly(t *testing.T) {
	sels := []string{"reviewed-node"}
	var got []string
	called := 0
	app, form, _ := newDrainFormTest(t, sels, func(_ ResourceViewer, selection []string, _ dao.DrainOptions) {
		called++
		got = selection
	})
	sels[0] = "different-node"
	app.Config.K9s.ReadOnly = true
	pressDrainButton(t, app, form, "OK")
	assert.Nil(t, got)
	assert.NotNil(t, app.Content.GetPrimitive(drainKey))
	app.Config.K9s.ReadOnly = false
	pressDrainButton(t, app, form, "OK")
	assert.Equal(t, []string{"reviewed-node"}, got)
	pressDrainButton(t, app, form, "OK")
	assert.Equal(t, 1, called)
}

func TestDrainRejectsDestinationChangesIncludingRoundTrips(t *testing.T) {
	for _, roundTrip := range []bool{false, true} {
		t.Run(strconv.FormatBool(roundTrip), func(t *testing.T) {
			called := 0
			app, form, modal := newDrainFormTest(t, []string{"reviewed-node"}, func(ResourceViewer, []string, dao.DrainOptions) { called++ })
			assert.Contains(t, drainDialogText(t, modal), "Context: ct-1-1")
			_, err := app.Config.ActivateContext("ct-1-2")
			require.NoError(t, err)
			if roundTrip {
				_, err := app.Config.ActivateContext("ct-1-1")
				require.NoError(t, err)
			}
			pressDrainButton(t, app, form, "OK")
			assert.Zero(t, called)
			assert.Same(t, modal, app.Content.GetPrimitive(drainKey))
			assert.Contains(t, drainDialogText(t, modal), "Destination changed.")
		})
	}
}

func TestDrainDoesNotOpenInReadOnlyMode(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	app.Config.K9s.ReadOnly = true
	b := NewBrowser(client.NodeGVR).(*Browser)
	b.app = app
	ShowDrain(b, []string{"fixture-node"}, dao.DrainOptions{}, func(ResourceViewer, []string, dao.DrainOptions) {
		t.Fatal("read-only drain was submitted")
	})
	assert.Nil(t, app.Content.GetPrimitive(drainKey))
}

func newDrainFormTest(t *testing.T, sels []string, callback DrainFunc) (*App, *tview.Form, *ui.ModalForm) {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	_, err := app.Config.ActivateContext("ct-1-1")
	require.NoError(t, err)
	b := NewBrowser(client.NodeGVR).(*Browser)
	b.app = app
	app.Content.Pages.AddPage("main", b, true, true)
	ShowDrain(b, sels, dao.DrainOptions{GracePeriodSeconds: -1, Timeout: 5 * time.Second}, callback)
	modal, ok := app.Content.GetPrimitive(drainKey).(*ui.ModalForm)
	require.True(t, ok)
	var form *tview.Form
	modal.Focus(func(p tview.Primitive) { form = p.(*tview.Form) })
	require.NotNil(t, form)
	return app, form, modal
}

func replaceDrainField(t *testing.T, app *App, form *tview.Form, index int, value string) {
	t.Helper()
	form.SetFocus(index)
	app.SetFocus(form)
	pressDrainKey(t, app, tcell.NewEventKey(tcell.KeyCtrlU, 0, tcell.ModNone))
	for _, r := range value {
		pressDrainKey(t, app, tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
}

func pressDrainButton(t *testing.T, app *App, form *tview.Form, label string) {
	t.Helper()
	index := form.GetButtonIndex(label)
	require.NotEqual(t, -1, index)
	form.SetFocus(form.GetFormItemCount() + index)
	app.SetFocus(form)
	pressDrainKey(t, app, tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
}

func pressDrainKey(t *testing.T, app *App, key *tcell.EventKey) {
	t.Helper()
	focus := app.GetFocus()
	require.NotNil(t, focus)
	focus.InputHandler()(key, func(p tview.Primitive) { app.SetFocus(p) })
}

func drainDialogText(t *testing.T, modal *ui.ModalForm) string {
	t.Helper()
	return strings.Join(strings.Fields(drawnText(t, modal, 120, 40)), " ")
}
