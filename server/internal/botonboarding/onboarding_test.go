package botonboarding

import (
	"strings"
	"testing"
)

func TestRenderOrdersOverviewThenCapabilities(t *testing.T) {
	out := Render()
	overview := strings.Index(out, "1. multica overview")
	capabilities := strings.Index(out, "2. multica capabilities")
	if overview < 0 || capabilities < 0 || overview > capabilities {
		t.Fatalf("steps out of order:\n%s", out)
	}
	for _, step := range Steps() {
		if !strings.Contains(out, step.Command) || !strings.Contains(out, step.Purpose) {
			t.Errorf("rendered text misses %+v", step)
		}
	}
}
