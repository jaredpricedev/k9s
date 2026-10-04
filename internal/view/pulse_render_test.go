// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/require"
)

func pulseLayoutFixture(t *testing.T) *Pulse {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	pulse := NewPulse(client.PuGVR).(*Pulse)
	require.NoError(t, pulse.Init(context.WithValue(context.Background(), internal.KeyApp, app)))
	return pulse
}

func TestPulseInitialFrameDoesNotPresentUnknownAsZero(t *testing.T) {
	p := pulseLayoutFixture(t)
	for _, size := range [][2]int{{120, 34}, {80, 24}, {60, 24}, {ui.MinTaskWidth, ui.MinTaskHeight}} {
		frame := drawnText(t, p, size[0], size[1])
		require.Contains(t, frame, "partial", "%dx%d", size[0], size[1])
		require.Contains(t, frame, "loading")
		require.Contains(t, frame, "--")
		require.NotContains(t, frame, "0/0", "zero is only shown after a completed read")
		require.NotContains(t, frame, "┌", "individual chart frames and the outer frame are removed")
		require.Contains(t, frame, "Enter browse")
	}
}

func TestPulseDrawThroughNativeApplicationDoesNotLockFocus(t *testing.T) {
	p := pulseLayoutFixture(t)
	screen := tcell.NewSimulationScreen("")
	require.NoError(t, screen.Init())
	screen.SetSize(80, 24)
	p.app.Application.SetScreen(screen).SetRoot(p, true)

	// The native draw lifecycle holds the application's lock. Looking up
	// Application.GetFocus inside a primitive's Draw deadlocks that lifecycle.
	drawn := make(chan struct{})
	go func() {
		p.app.Application.ForceDraw()
		close(drawn)
	}()
	select {
	case <-drawn:
	case <-time.After(time.Second):
		t.Fatal("Pulse draw blocked while the native application held its draw lock")
	}
	defer p.app.Application.Stop()
	var frame strings.Builder
	for y := range 24 {
		for x := range 80 {
			r, _, _, _ := screen.GetContent(x, y)
			frame.WriteRune(r)
		}
		frame.WriteByte('\n')
	}
	require.Contains(t, frame.String(), "Pulses /")
	require.Contains(t, frame.String(), "Selected: pods")
}

func TestPulseNativePagesRouteKeyboardToSelectedChart(t *testing.T) {
	p := pulseLayoutFixture(t)
	pages := tview.NewPages().AddPage("pulse", p, true, true)
	before := p.selectedIndex
	require.True(t, pages.HasFocus(), "the native root must recognize chart focus without calling Grid.Draw")
	pages.InputHandler()(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone), func(primitive tview.Primitive) {
		p.app.SetFocus(primitive)
	})
	require.Equal(t, before+1, p.selectedIndex)
	require.True(t, pages.HasFocus())
	require.Equal(t, p.charts[p.chartGVRs[p.selectedIndex]], p.app.GetFocus())
}

func TestPulsePartialReadAndSelectedIdentitySurviveResize(t *testing.T) {
	p := pulseLayoutFixture(t)
	now := time.Now()
	p.PulseChanged(model.HealthPoint{GVR: client.PodGVR, Namespace: p.model.GetNamespace(), State: model.HealthDenied, Message: "list pods access denied", CheckedAt: now})
	p.PulseChanged(model.HealthPoint{GVR: client.DpGVR, State: model.HealthAvailable, Total: 5, Faults: 1, Namespace: p.model.GetNamespace(), Source: "informer cache", ObservedAt: now, CheckedAt: now})
	p.PulseChanged(model.HealthPoint{GVR: client.PvcGVR, State: model.HealthEmpty, Namespace: p.model.GetNamespace(), Source: "informer cache", ObservedAt: now, CheckedAt: now})
	for _, size := range [][2]int{{120, 34}, {80, 24}, {60, 24}} {
		frame := drawnText(t, p, size[0], size[1])
		require.Contains(t, frame, "denied")
		require.Contains(t, frame, "5/1")
	}
	// Drive the real chart keyboard capture to a long resource label.
	for p.chartGVRs[p.selectedIndex] != client.PvcGVR {
		p.keyboard(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	}
	selected := p.selectedIndex
	for _, size := range [][2]int{{120, 34}, {80, 24}, {60, 24}, {40, 12}, {40, 8}, {60, 24}, {120, 34}} {
		frame := drawnText(t, p, size[0], size[1])
		if size[1] < ui.MinTaskHeight {
			require.Contains(t, frame, "View too small")
			require.Contains(t, frame, "Need 40x12")
		} else {
			require.Contains(t, frame, "Selected: persistentvolumeclaims")
			require.Contains(t, frame, "State: empty")
			require.Contains(t, frame, "informer cache")
			require.Contains(t, frame, "0/0", "an observed empty resource is distinct from unknown")
		}
		require.Equal(t, selected, p.selectedIndex)
		require.Equal(t, p.charts[client.PvcGVR], p.app.GetFocus())
	}
}

func TestPulseStaleRefreshPreservesCountsAndNamesWithoutColor(t *testing.T) {
	p := pulseLayoutFixture(t)
	now := time.Now()
	p.PulseChanged(model.HealthPoint{GVR: client.PodGVR, Namespace: p.model.GetNamespace(), State: model.HealthAvailable, Total: 10, Faults: 2, Source: "informer cache", ObservedAt: now, CheckedAt: now})
	p.PulseChanged(model.HealthPoint{GVR: client.PodGVR, Namespace: p.model.GetNamespace(), State: model.HealthUnavailable, Message: "cluster disconnected", CheckedAt: now})
	point := p.healthPoints[client.PodGVR]
	require.Equal(t, model.HealthStale, point.State)
	require.Equal(t, now, point.ObservedAt)
	frame := drawnText(t, p, 60, 24)
	require.Contains(t, frame, "stale / unavailable")
	require.Contains(t, frame, "10/2")
	require.Contains(t, frame, "cluster disconnected")
	require.Contains(t, frame, "> pods", "selection uses an ASCII marker")
	require.NotContains(t, frame, "┌")
	p.healthPoints[client.PodGVR] = model.HealthPoint{GVR: client.PodGVR, Namespace: p.model.GetNamespace(), State: model.HealthAvailable, Total: 10, Source: "informer cache", ObservedAt: now.Add(-time.Minute), CheckedAt: now.Add(-time.Minute)}
	frame = drawnText(t, p, 80, 24)
	require.Contains(t, frame, "stale / unavailable", "delayed collection ages even without a new result")
	for _, row := range strings.Split(frame, "\n") {
		require.NotContains(t, row, "healthy", "retained counts are not current health")
	}
}

func TestPulseIgnoresObservationsFromAnotherNamespace(t *testing.T) {
	p := pulseLayoutFixture(t)
	now := time.Now()
	p.PulseChanged(model.HealthPoint{GVR: client.PodGVR, Namespace: "another-namespace", State: model.HealthAvailable, Total: 99, ObservedAt: now, CheckedAt: now})
	require.Equal(t, model.HealthLoading, p.healthPoints[client.PodGVR].State)
	require.False(t, p.healthPoints[client.PodGVR].HasValue())
}
