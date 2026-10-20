package botonboarding

import (
	"strings"
	"testing"
)

func TestRenderStartsWithCapabilities(t *testing.T) {
	out := Render()
	if !strings.HasPrefix(out, "1. multica capabilities") {
		t.Fatalf("steps out of order:\n%s", out)
	}
	for _, step := range Steps() {
		if !strings.Contains(out, step.Command) || !strings.Contains(out, step.Purpose) {
			t.Errorf("rendered text misses %+v", step)
		}
	}
}
