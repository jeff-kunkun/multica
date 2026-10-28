package issuewatchdog

import (
	"strings"
	"testing"
	"time"
)

// The first lines are Codex's own wording on DENE-2670, 2672 and 2674
// (2026-10-27); none used a `verdict:` line.
func TestReviewVerdictReadsOfficeProse(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"DENE-2672 hold", "审查结论：需要修改，暂不通过，先请修正下面这项后再交 CTO·扳手签字。\n\n- [P2] Windows 原生 sandbox …", "hold"},
		{"DENE-2674 hold", "审查结论：需要修正后再签字（PR #11，`dene-2667-pr10-upstream-report`）。PR 的范围和只读设计清晰。", "hold"},
		{"DENE-2670 re-review hold", "复审 PR #14 当前提交 `6e603072`：**hold**，暂不交 CTO·扳手签字。\n\n仍有一项需要补齐", "hold"},
		{"DENE-2670 first hold", "审查结论：**hold**，暂不交 CTO·扳手签字；请修完以下问题后再审：\n\n1. **阻断：…**", "hold"},
		{"DENE-2670 pass", "复审通过 PR #14 当前提交 `fe18509c`，上一轮指出的类型遗漏已补齐。本次仅为类型声明变更，未发现新的阻断问题，可以交 CTO·扳手签字。", "pass"},
		{"DENE-2672 pass", "复审结论：代码复审通过，之前的 P2 已修复。\n\n我核对了 `7b50e79` 的实际实现", "pass"},
		{"DENE-2674 pass", "复审结论：Codex 复审通过，PR #11 当前提交 `7ead503f` 已修复上次指出的漏报问题。", "pass"},
		{"review plan is not a verdict", "我理解这次是对 Claude Code 的 PR #12 做合并前审查，重点确认权限默认值。\n\n审查步骤：", ""},
		{"manager relaying a hold is not a verdict", "【COO·Grok 代推进】[@Claude Code](mention://agent/51b3b400-dfb7-4eb7-b008-97ad701f6f12) Codex 17:27 复审 PR #14 给了 hold，还差一项。", ""},
		{"acceptance is not a review verdict", "【负责经理·扫帚】验收通过：PR #14 合并提交已在 kun。", ""},
		{"verdict line wins over prose", "审查结论：通过\n\nverdict: hold", "hold"},
		{"verdict pass line", "看过了。\nverdict: pass", "pass"},
		{"signed conclusion", "【审查·Codex】审查结论：打回，测试缺一条。", "hold"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := reviewVerdict(tc.content); got != tc.want {
				t.Fatalf("reviewVerdict(%q) = %q, want %q", tc.content, got, tc.want)
			}
		})
	}
}

func TestReviewTimelineStages(t *testing.T) {
	dev := actor{Type: "agent", ID: "11111111-1111-7111-8111-111111111111"}
	codex := actor{Type: "agent", ID: "22222222-2222-7222-8222-222222222222"}
	issue := Issue{AssigneeType: dev.Type, AssigneeID: dev.ID, ManagerName: "CTO·扳手"}
	base := time.Date(2026, 10, 27, 16, 0, 0, 0, time.UTC)
	n := 0
	c := func(author actor, content string) timelineComment {
		n++
		return timelineComment{ID: string(rune('a' + n)), Author: author, Content: content, CreatedAt: base.Add(time.Duration(n) * time.Minute)}
	}
	request := "PR 已推。[@Codex](mention://agent/" + codex.ID + ") 请复审。"

	tests := []struct {
		name      string
		comments  []timelineComment
		stage     reviewStage
		reviewer  actor
		lastStage bool
	}{
		{"request without a verdict waits on the reviewer",
			[]timelineComment{c(dev, request)}, stageAwaitingReview, codex, true},
		{"hold after the request is the developer's",
			[]timelineComment{c(dev, request), c(codex, "审查结论：需要修改，暂不通过。")}, stageHold, codex, true},
		{"a bare 收到 after the hold keeps it last",
			[]timelineComment{c(codex, "审查结论：hold。"), c(dev, "收到")}, stageHold, codex, true},
		{"the developer answering a hold ends it",
			[]timelineComment{c(codex, "审查结论：hold。"), c(dev, "在改了")}, stageWorking, actor{}, false},
		{"pass waits on sign-off",
			[]timelineComment{c(dev, request), c(codex, "复审通过，可以交 CTO·扳手签字。")}, stageAwaitingSignoff, codex, true},
		{"a signed manager reply ends sign-off",
			[]timelineComment{c(codex, "复审通过。"), c(actor{Type: "member", ID: "33333333-3333-7333-8333-333333333333"}, "【负责经理·扳手】签了，合吧")},
			stageWorking, actor{}, false},
		{"a request that mentions nobody is not a request",
			[]timelineComment{c(dev, "请复审")}, stageWorking, actor{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := reviewTimeline(issue, tc.comments)
			if st.Stage != tc.stage || st.LastIsStage != tc.lastStage {
				t.Fatalf("stage=%v last=%v, want %v last=%v", st.Stage, st.LastIsStage, tc.stage, tc.lastStage)
			}
			if tc.stage != stageWorking && st.Reviewer != tc.reviewer {
				t.Fatalf("reviewer = %+v, want %+v", st.Reviewer, tc.reviewer)
			}
		})
	}

	withField := issue
	withField.Reviewer = actor{Type: "member", ID: "44444444-4444-7444-8444-444444444444"}
	if st := reviewTimeline(withField, []timelineComment{c(dev, request)}); st.Reviewer != withField.Reviewer {
		t.Fatalf("the issue's reviewer field should win over the mention: %+v", st.Reviewer)
	}
}

func TestFailedReviewRoundsStopAtTheLimit(t *testing.T) {
	dev := actor{Type: "agent", ID: "11111111-1111-7111-8111-111111111111"}
	codex := actor{Type: "agent", ID: "22222222-2222-7222-8222-222222222222"}
	issue := Issue{AssigneeType: dev.Type, AssigneeID: dev.ID, ManagerName: "CTO·扳手"}
	base := time.Date(2026, 10, 27, 16, 0, 0, 0, time.UTC)
	n := 0
	c := func(author actor, content string) timelineComment {
		n++
		return timelineComment{ID: string(rune('a' + n)), Author: author, Content: content, CreatedAt: base.Add(time.Duration(n) * time.Minute)}
	}
	hold := func() timelineComment { return c(codex, "审查结论：hold，暂不交 CTO·扳手签字。") }
	request := func() timelineComment {
		return c(dev, "已修复。[@Codex](mention://agent/"+codex.ID+") 请复审。")
	}

	tests := []struct {
		name     string
		comments func() []timelineComment
		limit    int
		rounds   int
		stopAt   int // index into comments of the hold that stops, -1 for none
	}{
		{"two failed rounds stop at the second hold", func() []timelineComment {
			return []timelineComment{hold(), request(), hold()}
		}, 2, 2, 2},
		{"a second hold in the same round is not a new round", func() []timelineComment {
			return []timelineComment{hold(), hold()}
		}, 2, 1, -1},
		{"asking for another review lifts the stop", func() []timelineComment {
			return []timelineComment{hold(), request(), hold(), request()}
		}, 2, 2, -1},
		{"a pass starts the count over", func() []timelineComment {
			return []timelineComment{hold(), request(), c(codex, "复审通过。"), request(), hold()}
		}, 2, 1, -1},
		{"the developer answering without a request still ends a round", func() []timelineComment {
			return []timelineComment{hold(), c(dev, "这条是误判，原因如下"), hold()}
		}, 2, 2, 2},
		{"a third round after the stop stops again", func() []timelineComment {
			return []timelineComment{hold(), request(), hold(), request(), hold()}
		}, 2, 3, 4},
		{"the limit is configurable", func() []timelineComment {
			return []timelineComment{hold(), request(), hold()}
		}, 3, 2, -1},
		{"a bare 收到 does not end a round", func() []timelineComment {
			return []timelineComment{hold(), c(dev, "收到"), hold()}
		}, 2, 1, -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			comments := tc.comments()
			st := reviewTimeline(issue, comments)
			if len(st.FailedRounds) != tc.rounds {
				t.Fatalf("failed rounds = %d, want %d", len(st.FailedRounds), tc.rounds)
			}
			got, stop := st.roundLimitHold(tc.limit)
			if tc.stopAt < 0 {
				if stop {
					t.Fatalf("stopped at %s, want no stop", got.ID)
				}
				return
			}
			if !stop || got.ID != comments[tc.stopAt].ID {
				t.Fatalf("stop=%v at %q, want stop at %q", stop, got.ID, comments[tc.stopAt].ID)
			}
		})
	}
}

func TestRoundLimitNoteListsRoundsAndOptions(t *testing.T) {
	rounds := []timelineComment{
		{Content: "【审查·Codex】审查结论：**hold**，暂不交 CTO·扳手签字；请修完以下问题后再审：\n\n1. 阻断"},
		{Content: "复审 PR #14 当前提交 `6e603072`：**hold**，暂不交 CTO·扳手签字。\n\n仍有一项需要补齐"},
	}
	note := roundLimitNote(Issue{ManagerName: "CTO·扳手"}, rounds)
	for _, want := range []string{"连续 2 轮不通过", "请 Kun 拍板",
		"1. 审查结论：hold，暂不交 CTO·扳手签字；请修完以下问题后再审：",
		"2. 复审 PR #14 当前提交 `6e603072`：hold，暂不交 CTO·扳手签字。",
		"A. 再改一轮", "B. 先合并", "C. 请 CTO·扳手 裁决", "D. 放弃这个改动"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note is missing %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, "mention://") {
		t.Fatalf("the note must not @mention anyone:\n%s", note)
	}
}

func TestReviewWakesAreOffByDefault(t *testing.T) {
	for _, name := range []string{"ENABLED", "HOLD_WAKE", "HOLD_WAIT", "REVIEW_WAKE", "REVIEW_WAIT",
		"STUCK_WAKE", "NO_RUN_WAIT", "CHILD_WAKE", "CHILD_WAIT", "ROUND_STOP", "ROUND_LIMIT"} {
		t.Setenv("MULTICA_ISSUE_WATCHDOG_"+name, "")
	}
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HoldWake || cfg.ReviewWake || cfg.StuckWake || cfg.ChildWake || cfg.RoundStop {
		t.Fatalf("review wakes on by default: %+v", cfg)
	}
	if cfg.RoundLimit != 2 {
		t.Fatalf("default round limit = %d, want 2", cfg.RoundLimit)
	}
	t.Setenv("MULTICA_ISSUE_WATCHDOG_ROUND_LIMIT", "3")
	if cfg, err := ConfigFromEnv(); err != nil || cfg.RoundLimit != 3 {
		t.Fatalf("round limit 3: %d, %v", cfg.RoundLimit, err)
	}
	t.Setenv("MULTICA_ISSUE_WATCHDOG_ROUND_LIMIT", "0")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("a round limit of 0 was accepted")
	}
	t.Setenv("MULTICA_ISSUE_WATCHDOG_ROUND_LIMIT", "")
	if cfg.HoldWait != 2*time.Hour || cfg.ReviewWait != 20*time.Minute ||
		cfg.NoRunWait != 15*time.Minute || cfg.ChildWait != 30*time.Minute {
		t.Fatalf("default waits = %v %v %v %v", cfg.HoldWait, cfg.ReviewWait, cfg.NoRunWait, cfg.ChildWait)
	}
	t.Setenv("MULTICA_ISSUE_WATCHDOG_STUCK_WAKE", "maybe")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("a non-boolean switch was accepted")
	}
}
