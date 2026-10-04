// Modified for k9+; see NOTICE.
package tchart_test

import (
	"strings"
	"testing"

	"github.com/derailed/k9s/internal/tchart"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/require"
)

func TestGaugeUnknownAndLargeValuesStayInsideTheirRectangle(t *testing.T) {
	screen := tcell.NewSimulationScreen("")
	require.NoError(t, screen.Init())
	defer screen.Fini()
	screen.SetSize(40, 16)
	gauge := tchart.NewGauge("pods")
	gauge.SetRect(10, 4, 18, 8)
	gauge.Add(1234567, 9876543)
	for _, status := range []string{"denied", ""} {
		gauge.SetStatus(status)
		for y := range 16 {
			for x := range 40 {
				screen.SetContent(x, y, '.', nil, tcell.StyleDefault)
			}
		}
		gauge.Draw(screen)
		var text strings.Builder
		for y := range 16 {
			for x := range 40 {
				ch, _, _, _ := screen.GetContent(x, y)
				if x < 10 || x >= 28 || y < 4 || y >= 12 {
					require.Equal(t, '.', ch, "chart wrote outside its rectangle at %d,%d", x, y)
				}
				text.WriteRune(ch)
			}
		}
		if status != "" {
			require.Contains(t, text.String(), "denied")
		} else {
			require.Contains(t, text.String(), "1234567 total")
		}
	}
}
