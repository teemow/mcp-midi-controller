package measure_test

import (
	"testing"

	"github.com/teemow/mcp-midi-controller/internal/craft"
	"github.com/teemow/mcp-midi-controller/internal/measure"
)

// Measure reads every measurement of craft's closed list, so a measurement
// added there without code here fails. The test sits in its own package: craft
// may import measure without a cycle.
func TestMeasureReadsCraftsMeasurements(t *testing.T) {
	sig := measure.Signal{Channels: 2, SampleRate: 48000}
	for i := range 4 * 48000 {
		x := float32(0.1 * float64(i%97) / 97)
		sig.Samples = append(sig.Samples, x, -x/2)
	}
	for _, m := range craft.Measurements() {
		var band []float64
		if m == craft.MeasureBandLevel {
			band = []float64{100, 1000}
		}
		if _, err := measure.Measure(sig, string(m), band); err != nil {
			t.Errorf("%s: %v", m, err)
		}
	}
}
