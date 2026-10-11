package service

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// A chat turn killed by runtime_offline is not retried (DENE-1839): the retry
// would wait deferred for the same machine while the sender was already told.
// Issue runs keep the deferred retry, and chat turns keep retries for every
// other retryable reason.
func TestRetryGatesOpen_ChatRuntimeOfflineIsFinal(t *testing.T) {
	chat := db.AgentTaskQueue{
		Attempt: 1, MaxAttempts: 3,
		ChatSessionID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true},
	}
	issue := db.AgentTaskQueue{
		Attempt: 1, MaxAttempts: 3,
		IssueID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
	}
	offline := string(taskfailure.ReasonRuntimeOffline)
	if retryGatesOpen(offline, chat, retryEnabledAgent()) {
		t.Fatal("chat turn should not be retried after runtime_offline")
	}
	if retryEligible(offline, chat, retryEnabledAgent()) {
		t.Fatal("retryEligible must agree: chat runtime_offline is final")
	}
	if !retryGatesOpen(offline, issue, retryEnabledAgent()) {
		t.Fatal("issue run should keep its deferred runtime_offline retry")
	}
	if !retryGatesOpen(string(taskfailure.ReasonTimeout), chat, retryEnabledAgent()) {
		t.Fatal("chat turns still retry other retryable reasons")
	}
}

func TestChatRuntimeOfflineNotice(t *testing.T) {
	now := time.Date(2026, 10, 11, 14, 10, 0, 0, time.UTC)
	rt := db.AgentRuntime{
		Name:       "kun-mac",
		CustomName: pgtype.Text{String: "Kun 的 Mac", Valid: true},
		LastSeenAt: pgtype.Timestamptz{Time: now.Add(-5 * time.Minute), Valid: true},
	}
	got := chatRuntimeOfflineNotice(rt, now)
	for _, want := range []string{"Kun 的 Mac", "2026-10-11 14:05 UTC", "约 5 分钟 前", "请再发一次"} {
		if !strings.Contains(got, want) {
			t.Fatalf("notice %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "kun-mac") {
		t.Fatalf("custom name should replace the raw name: %q", got)
	}

	long := chatRuntimeOfflineNotice(db.AgentRuntime{
		Name:       "box",
		LastSeenAt: pgtype.Timestamptz{Time: now.Add(-(2*time.Hour + 7*time.Minute)), Valid: true},
	}, now)
	if !strings.Contains(long, "约 2 小时 7 分钟 前") {
		t.Fatalf("hours should render in Chinese: %q", long)
	}

	bare := chatRuntimeOfflineNotice(db.AgentRuntime{Name: "box"}, now)
	if !strings.Contains(bare, "「box」已掉线") || strings.Contains(bare, "最后在线") {
		t.Fatalf("runtime without a heartbeat should skip the last-seen clause: %q", bare)
	}
}
