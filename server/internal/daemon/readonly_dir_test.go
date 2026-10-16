package daemon

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestReadonlyDirArgs(t *testing.T) {
	mk := func(daemon, path string) ProjectResourceData {
		raw, _ := json.Marshal(map[string]string{"daemon_id": daemon, "local_path": path})
		return ProjectResourceData{ResourceType: "readonly_directory", ResourceRef: raw}
	}
	got := readonlyDirArgs([]ProjectResourceData{
		mk("d1", "/a"), mk("other", "/b"), {ResourceType: "local_directory", ResourceRef: json.RawMessage(`{"local_path":"/c","daemon_id":"d1"}`)},
	}, "d1")
	if want := []string{"--add-dir", "/a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
