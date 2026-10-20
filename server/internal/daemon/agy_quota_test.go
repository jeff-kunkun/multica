package daemon

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAgyQuotaSnapshotDropsExpired(t *testing.T) {
	home := t.TempDir()
	agyQuotaFileFn = func() string { return filepath.Join(home, "quota.json") }
	t.Cleanup(func() { agyQuotaFileFn = defaultAgyQuotaFile })

	d := &Daemon{}
	now := time.Unix(1_800_000_000, 0)
	dir := filepath.Join(home, ".gemini")
	d.markAccountQuotaExhausted(dir, now.Add(-time.Minute))
	if states := d.accountQuotaSnapshot(now); len(states) != 0 {
		t.Fatalf("expired still present: %#v", states)
	}
}
