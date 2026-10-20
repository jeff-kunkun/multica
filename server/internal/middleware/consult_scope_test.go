package middleware

import (
	"net/http"
	"testing"
)

func TestConsultScopeIsReadOnly(t *testing.T) {
	if !isConsultReadOnlyScope(ConsultTaskKind) {
		t.Fatal("a consult token must be read-only")
	}
	for _, kind := range []string{"", "issue", "chat", "quick_create"} {
		if isConsultReadOnlyScope(kind) {
			t.Errorf("%q should not be read-only", kind)
		}
	}
}

// Method classification is an allowlist, so a verb nobody classified is
// refused rather than treated as a read.
func TestIsReadOnlyMethod(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		if !isReadOnlyMethod(method) {
			t.Fatalf("isReadOnlyMethod(%q) = false, want true", method)
		}
	}
	refused := []string{
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
		http.MethodConnect,
		http.MethodTrace,
		"PURGE",
		"",
	}
	for _, method := range refused {
		if isReadOnlyMethod(method) {
			t.Fatalf("isReadOnlyMethod(%q) = true, want false", method)
		}
	}
}
