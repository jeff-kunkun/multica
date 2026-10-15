package handler

import (
	"encoding/json"
	"testing"
)

func TestValidateReadonlyDirectoryRef(t *testing.T) {
	ok, err := validateAndNormalizeResourceRef("readonly_directory", json.RawMessage(`{"daemon_id":" d1 ","local_path":" /Volumes/x "}`))
	if err != nil || string(ok) != `{"daemon_id":"d1","local_path":"/Volumes/x"}` {
		t.Fatalf("got %s %v", ok, err)
	}
	for _, bad := range []string{`{"daemon_id":"d1","local_path":"rel"}`, `{"local_path":"/x"}`, `{}`} {
		if _, err := validateAndNormalizeResourceRef("readonly_directory", json.RawMessage(bad)); err == nil {
			t.Fatalf("expected error for %s", bad)
		}
	}
}
