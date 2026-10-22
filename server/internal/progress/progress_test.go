package progress

import (
	"strings"
	"testing"
)

func TestNormalizeTone(t *testing.T) {
	for in, want := range map[string]string{"": "", " Stuck ": ToneStuck, "done": ToneDone} {
		if got, ok := NormalizeTone(in); !ok || got != want {
			t.Errorf("NormalizeTone(%q) = %q, %v", in, got, ok)
		}
	}
	if _, ok := NormalizeTone("red"); ok {
		t.Error("unknown tone accepted")
	}
}

func TestForStatus(t *testing.T) {
	for status, want := range map[string]string{
		"done": ToneDone, "cancelled": ToneDone, "blocked": ToneStuck,
		"in_review": ToneWaiting, "in_progress": ToneWorking, "todo": ToneWorking,
	} {
		if got := ForStatus(status); got != want {
			t.Errorf("ForStatus(%q) = %q, want %q", status, got, want)
		}
	}
}

func TestClip(t *testing.T) {
	if got := Clip("  a \n b  "); got != "a b" {
		t.Errorf("Clip collapsed to %q", got)
	}
	if got := Clip(strings.Repeat("字", MaxLen+5)); len([]rune(got)) != MaxLen {
		t.Errorf("Clip length = %d", len([]rune(got)))
	}
}
