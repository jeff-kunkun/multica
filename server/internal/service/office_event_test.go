package service

import (
	"strings"
	"testing"
	"time"
)

func statusFacts(from, to, actorType, source, title string) OfficeFacts {
	return OfficeFacts{
		Family: "issue", EventType: "issue.status_changed", ActorType: actorType, ActorSource: source,
		IssueTitle: title, IssueKey: "DENE-1",
		Changes: []OfficeChange{{Field: "status", Old: from, New: to}},
	}
}

func TestClassifyOfficeTellsRoundHandoffAndWholeTaskApart(t *testing.T) {
	cases := []struct {
		name      string
		facts     OfficeFacts
		meaning   string
		mustRelay bool
	}{
		{"one run finished", OfficeFacts{Family: "issue", EventType: "run.completed"}, OfficeMeaningRoundDone, false},
		{"handed over", statusFacts("in_progress", "in_review", "agent", "", "x"), OfficeMeaningHandoff, true},
		{"whole task done", statusFacts("in_review", "done", "member", "", "x"), OfficeMeaningTaskDone, true},
		{"stuck", statusFacts("in_progress", "blocked", "agent", "", "x"), OfficeMeaningBlocked, true},
		{"plain move", statusFacts("todo", "in_progress", "agent", "", "x"), OfficeMeaningChange, false},
		{"chat answer", OfficeFacts{Family: "chat", EventType: "chat.replied"}, OfficeMeaningReply, true},
		{"chat run failed", OfficeFacts{Family: "chat", EventType: "run.failed"}, OfficeMeaningFailed, true},
		{"ticket run failed", OfficeFacts{Family: "issue", EventType: "run.failed"}, OfficeMeaningFailed, false},
		{"agent file", OfficeFacts{Family: "artifact", EventType: "artifact.attached", UploaderType: "agent"}, OfficeMeaningArtifact, true},
		{"person's file", OfficeFacts{Family: "artifact", EventType: "artifact.attached", UploaderType: "member"}, OfficeMeaningArtifact, false},
		{"setting", OfficeFacts{Family: "config", EventType: "config.agent.update", Changes: []OfficeChange{{Field: "model"}}}, OfficeMeaningChange, false},
	}
	for _, c := range cases {
		got := ClassifyOffice(c.facts)
		if got.Meaning != c.meaning || got.MustRelay != c.mustRelay || got.PendingApproval {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

func TestClassifyOfficeRoutingDoneOnReleaseWorkIsNotAPermission(t *testing.T) {
	got := ClassifyOffice(statusFacts("in_review", "done", "system", "routing", "正式发布 v2"))
	if !got.PendingApproval || !got.MustRelay || got.Meaning != OfficeMeaningPendingRel ||
		!strings.Contains(got.Summary, "系统改成完成，正式发布仍待批准") {
		t.Fatalf("%+v", got)
	}
	// An agent's close, a reviewer's verdict (an agent) and a Bot acting for
	// its owner are not the person's approval either.
	viaBot := statusFacts("in_review", "done", "member", "", "正式发布 v2")
	viaBot.ViaBot = true
	for _, f := range []OfficeFacts{
		statusFacts("in_review", "done", "agent", "", "正式发布 v2"),
		statusFacts("in_review", "done", "system", "", "正式发布 v2"),
		viaBot,
	} {
		if got := ClassifyOffice(f); !got.PendingApproval || got.Meaning != OfficeMeaningPendingRel || !got.MustRelay {
			t.Errorf("%+v sealed as %+v", f, got)
		}
	}
	for _, f := range []OfficeFacts{
		statusFacts("in_review", "done", "member", "", "正式发布 v2"),     // a person decided
		statusFacts("in_review", "done", "system", "routing", "整理文档"), // not release work
	} {
		if got := ClassifyOffice(f); got.PendingApproval {
			t.Errorf("%+v flagged pending approval", f)
		}
	}
}

func TestIsReleaseWork(t *testing.T) {
	for _, s := range []string{"正式发布 1.2", "发版", "部署到生产环境", "Release v3", "deploy to prod", "go-live checklist"} {
		if !IsReleaseWork(s, "") {
			t.Errorf("%q not release work", s)
		}
	}
	for _, s := range []string{"整理会议纪要", "relationship notes", "fix login button"} {
		if IsReleaseWork(s, "") {
			t.Errorf("%q flagged as release work", s)
		}
	}
}

func TestConfigSummaryNamesTheObjectAndFields(t *testing.T) {
	got := ClassifyOffice(OfficeFacts{EventType: "config.agent.update", Changes: []OfficeChange{{Field: "model"}, {Field: "instructions"}}})
	if got.Summary != "智能体修改：model、instructions" {
		t.Fatalf("%q", got.Summary)
	}
}

func TestOfficeDeliveryNeverGivesUp(t *testing.T) {
	if OfficeDeliveryBackoff(0) != 15*time.Second || OfficeDeliveryBackoff(1000) != 30*time.Minute {
		t.Fatal("backoff")
	}
	if OfficeReceiptTimeout(0) != 2*time.Minute || OfficeReceiptTimeout(50) != 30*time.Minute {
		t.Fatal("receipt timeout")
	}
}
