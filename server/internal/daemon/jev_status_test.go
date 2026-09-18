package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// writeFile writes a state file and pins its mtime so observed_at assertions
// are deterministic.
func writeJevFile(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

func TestReadJevStatusSnapshot(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	older := now.Add(-2 * time.Hour)
	newer := now.Add(-1 * time.Hour)

	t.Run("missing directory is unknown", func(t *testing.T) {
		got := readJevStatusSnapshot(filepath.Join(t.TempDir(), "absent"), now)
		if got.Status != protocol.JevStatusUnknown || got.ObservedAt != 0 {
			t.Fatalf("snapshot = %+v, want unknown/0", got)
		}
	})

	t.Run("missing breaker is unknown even with a decision log", func(t *testing.T) {
		dir := t.TempDir()
		writeJevFile(t, filepath.Join(dir, jevDecisionsFile),
			`{"scene":"pr-risk","outcome":"@1","model":"jev-1.13.0","at":"2026-09-18T10:58:12+0800"}`+"\n", older)
		got := readJevStatusSnapshot(dir, now)
		if got.Status != protocol.JevStatusUnknown || got.Model != "" {
			t.Fatalf("snapshot = %+v, want unknown with no detail", got)
		}
	})

	t.Run("corrupt breaker is unknown", func(t *testing.T) {
		dir := t.TempDir()
		writeJevFile(t, filepath.Join(dir, jevBreakerFile), "{not json", older)
		got := readJevStatusSnapshot(dir, now)
		if got.Status != protocol.JevStatusUnknown || got.ObservedAt != 0 {
			t.Fatalf("snapshot = %+v, want unknown/0", got)
		}
	})

	t.Run("healthy breaker is active and carries the decision tail", func(t *testing.T) {
		dir := t.TempDir()
		writeJevFile(t, filepath.Join(dir, jevBreakerFile),
			`{"failures":0,"disabled_until":0,"manual":false,"reason":""}`, older)
		writeJevFile(t, filepath.Join(dir, jevDecisionsFile),
			`{"scene":"pr-risk","outcome":"@1","model":"jev-1.13.0","at":"2026-09-18T10:58:12+0800"}`+"\n"+
				`{"scene":"browser-evidence","outcome":"not_evidence","model":"jev-1.13.0","at":"2026-09-18T11:02:00+0800"}`+"\n",
			newer)

		got := readJevStatusSnapshot(dir, now)
		if got.Status != protocol.JevStatusActive {
			t.Fatalf("status = %q, want active", got.Status)
		}
		if got.Model != "jev-1.13.0" || got.LastScene != "browser-evidence" || got.LastOutcome != "not_evidence" {
			t.Fatalf("last decision = %+v", got)
		}
		wantAt := time.Date(2026, 9, 18, 11, 2, 0, 0, time.FixedZone("", 8*3600)).Unix()
		if got.LastDecisionAt != wantAt {
			t.Fatalf("last_decision_at = %d, want %d", got.LastDecisionAt, wantAt)
		}
		// observed_at is the newest of the two mtimes, never time.Now().
		if got.ObservedAt != newer.Unix() {
			t.Fatalf("observed_at = %d, want %d", got.ObservedAt, newer.Unix())
		}
	})

	t.Run("manual disable is fallback with the stored reason", func(t *testing.T) {
		dir := t.TempDir()
		writeJevFile(t, filepath.Join(dir, jevBreakerFile),
			`{"failures":2,"disabled_until":0,"manual":true,"reason":"额度见底"}`, older)
		got := readJevStatusSnapshot(dir, now)
		if got.Status != protocol.JevStatusFallback || got.Reason != "额度见底" || !got.Manual {
			t.Fatalf("snapshot = %+v, want fallback/额度见底/manual", got)
		}
	})

	t.Run("manual disable without a reason gets the CLI default", func(t *testing.T) {
		dir := t.TempDir()
		writeJevFile(t, filepath.Join(dir, jevBreakerFile),
			`{"failures":0,"disabled_until":0,"manual":true,"reason":""}`, older)
		got := readJevStatusSnapshot(dir, now)
		if got.Status != protocol.JevStatusFallback || got.Reason != jevDefaultManualReason {
			t.Fatalf("snapshot = %+v, want fallback/%s", got, jevDefaultManualReason)
		}
	})

	t.Run("active cooldown is fallback with the stored reason", func(t *testing.T) {
		dir := t.TempDir()
		writeJevFile(t, filepath.Join(dir, jevBreakerFile),
			`{"failures":3,"disabled_until":1800000900,"manual":false,"reason":"连续 3 次调用失败"}`, older)
		got := readJevStatusSnapshot(dir, now)
		if got.Status != protocol.JevStatusFallback || got.Reason != "连续 3 次调用失败" {
			t.Fatalf("snapshot = %+v, want fallback with reason", got)
		}
		if got.DisabledUntil != 1_800_000_900 {
			t.Fatalf("disabled_until = %d", got.DisabledUntil)
		}
	})

	t.Run("cooldown without a reason gets the CLI default", func(t *testing.T) {
		dir := t.TempDir()
		writeJevFile(t, filepath.Join(dir, jevBreakerFile),
			`{"failures":3,"disabled_until":1800000900,"manual":false,"reason":""}`, older)
		got := readJevStatusSnapshot(dir, now)
		if got.Status != protocol.JevStatusFallback || got.Reason != jevDefaultCooldownReason {
			t.Fatalf("snapshot = %+v, want fallback/%s", got, jevDefaultCooldownReason)
		}
	})

	// The cooldown lapsing with the files untouched is the whole reason the
	// daemon re-evaluates every tick instead of trusting a stored status.
	t.Run("expired cooldown flips back to active without a file change", func(t *testing.T) {
		dir := t.TempDir()
		writeJevFile(t, filepath.Join(dir, jevBreakerFile),
			`{"failures":3,"disabled_until":1799999999,"manual":false,"reason":"上游连续失败"}`, older)
		got := readJevStatusSnapshot(dir, now)
		if got.Status != protocol.JevStatusActive || got.Reason != "" {
			t.Fatalf("snapshot = %+v, want active", got)
		}
	})

	t.Run("decision tail read survives a partial final line", func(t *testing.T) {
		dir := t.TempDir()
		writeJevFile(t, filepath.Join(dir, jevBreakerFile),
			`{"failures":0,"disabled_until":0,"manual":false,"reason":""}`, older)
		writeJevFile(t, filepath.Join(dir, jevDecisionsFile),
			`{"scene":"a","outcome":"@1","model":"jev-1.13.0","at":"2026-09-18T10:58:12+0800"}`+"\n"+`{"scene":"b","outcome":"@2","mode`,
			newer)
		got := readJevStatusSnapshot(dir, now)
		if got.LastScene != "a" || got.LastOutcome != "@1" {
			t.Fatalf("snapshot = %+v, want the last complete line", got)
		}
	})

	t.Run("decision tail read ignores a very large history", func(t *testing.T) {
		dir := t.TempDir()
		writeJevFile(t, filepath.Join(dir, jevBreakerFile),
			`{"failures":0,"disabled_until":0,"manual":false,"reason":""}`, older)
		var b strings.Builder
		for i := 0; i < 400; i++ {
			b.WriteString(`{"scene":"filler","outcome":"@1","model":"old","at":"2026-09-18T09:00:00+0800"}` + "\n")
		}
		b.WriteString(`{"scene":"tail","outcome":"@9","model":"jev-1.13.0","at":"2026-09-18T11:02:00+0800"}` + "\n")
		writeJevFile(t, filepath.Join(dir, jevDecisionsFile), b.String(), newer)
		if int64(b.Len()) <= jevDecisionTailBytes {
			t.Fatalf("fixture is only %d bytes; must exceed the %d-byte tail window", b.Len(), jevDecisionTailBytes)
		}
		got := readJevStatusSnapshot(dir, now)
		if got.LastScene != "tail" || got.Model != "jev-1.13.0" {
			t.Fatalf("snapshot = %+v, want the final line", got)
		}
	})

	// Known gap, recorded deliberately: the jev CLI also honours a JEV_DISABLED
	// environment variable, which is not visible to the daemon's file read. An
	// env-disabled host therefore still reports active.
	t.Run("JEV_DISABLED environment variable is not observed", func(t *testing.T) {
		t.Setenv("JEV_DISABLED", "1")
		dir := t.TempDir()
		writeJevFile(t, filepath.Join(dir, jevBreakerFile),
			`{"failures":0,"disabled_until":0,"manual":false,"reason":""}`, older)
		got := readJevStatusSnapshot(dir, now)
		if got.Status != protocol.JevStatusActive {
			t.Fatalf("snapshot = %+v, want active (env path is out of scope)", got)
		}
	})
}

func TestJevStateDirFollowsXdgThenHome(t *testing.T) {
	d := &Daemon{}
	t.Setenv("XDG_STATE_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	if got := d.jevStateDir(); got != filepath.Join(home, ".local", "state", "jev") {
		t.Fatalf("default state dir = %q", got)
	}

	xdg := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdg)
	if got := d.jevStateDir(); got != filepath.Join(xdg, "jev") {
		t.Fatalf("xdg state dir = %q", got)
	}

	d.jevStateDirOverride = "/fixture/state"
	t.Setenv("XDG_STATE_HOME", xdg)
	if got := d.jevStateDir(); got != "/fixture/state" {
		t.Fatalf("override state dir = %q", got)
	}
}
