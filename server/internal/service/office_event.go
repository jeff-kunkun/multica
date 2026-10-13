package service

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The office event stream (DENE-1963). Rows are captured by triggers in the
// write's own transaction (migration 687); this file holds the pure rules the
// dispatcher applies when it seals a row: what the event means to a person,
// whether it must reach them (must_relay), and whether a "done" still waits
// for a release approval nobody gave.

const OfficeEventSchema = "multica.office_event/v1"

// Meanings, in words a Bot can show.
const (
	OfficeMeaningReply      = "reply"      // an answer arrived in a chat
	OfficeMeaningRoundDone  = "round_done" // one run finished; the work may go on
	OfficeMeaningHandoff    = "handoff"    // handed over for review / to someone else
	OfficeMeaningTaskDone   = "task_done"  // the ticket is finished
	OfficeMeaningBlocked    = "blocked"    // stopped, needs someone
	OfficeMeaningFailed     = "failed"     // a run failed or was cancelled
	OfficeMeaningArtifact   = "artifact"   // a file was delivered
	OfficeMeaningChange     = "change"     // a status, setting, member or config moved
	OfficeMeaningPendingRel = "release_pending_approval"
)

// OfficeFacts is what the sealer knows about one captured row.
type OfficeFacts struct {
	Family      string
	EventType   string
	Changes     []OfficeChange
	ActorType   string
	ActorSource string
	// ViaBot: a person's identity used by their Bot, not the person's own hand.
	ViaBot bool
	// The ticket's own words, for spotting release work. Empty for chats.
	IssueTitle       string
	IssueDescription string
	// Files: who uploaded (agent / member).
	UploaderType string
	Filename     string
	ChatTitle    string
	IssueKey     string
}

// OfficeChange is one field that moved. Values are already masked.
type OfficeChange struct {
	Field  string `json:"field"`
	Old    any    `json:"old"`
	New    any    `json:"new"`
	Masked bool   `json:"masked,omitempty"`
}

// OfficeClass is the sealed reading of an event.
type OfficeClass struct {
	Meaning         string
	MustRelay       bool
	PendingApproval bool
	Summary         string
}

// ParseOfficeChanges reads the changes column.
func ParseOfficeChanges(raw []byte) []OfficeChange {
	var out []OfficeChange
	if len(raw) == 0 || json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

func (f OfficeFacts) change(field string) (old, new string, ok bool) {
	for _, c := range f.Changes {
		if c.Field == field {
			o, _ := c.Old.(string)
			n, _ := c.New.(string)
			return o, n, true
		}
	}
	return "", "", false
}

// releaseWords marks a ticket as release work: shipping to production, a
// formal release, a deploy. Text is the only signal there is (no structured
// release approval exists), so it errs toward flagging.
var releaseWords = regexp.MustCompile(`(?i)(正式发布|正式版|发布上线|上线|发版|生产环境|生产发布|投产|部署到生产|\bprod(uction)?\b|\brelease\b|\bdeploy(ment)?\b|\bgo[- ]live\b|\bship\b)`)

// IsReleaseWork reports whether a ticket's own words say it is about a
// production or formal release.
func IsReleaseWork(title, description string) bool {
	return releaseWords.MatchString(title) || releaseWords.MatchString(description)
}

func (f OfficeFacts) personsOwnHand() bool {
	return f.ActorType == "member" && !f.ViaBot
}

// ClassifyOffice applies the rules. Pure: tests drive it directly.
func ClassifyOffice(f OfficeFacts) OfficeClass {
	label := f.IssueKey
	if label == "" {
		label = f.IssueTitle
	}
	if label == "" {
		label = f.ChatTitle
	}
	switch f.EventType {
	case "chat.replied":
		return OfficeClass{Meaning: OfficeMeaningReply, MustRelay: true, Summary: joinSummary("聊天有新回复", f.ChatTitle)}
	case "run.completed":
		return OfficeClass{Meaning: OfficeMeaningRoundDone, Summary: joinSummary("本轮完成", label)}
	case "run.failed":
		// A chat waits for its answer; a failed chat run is the answer.
		return OfficeClass{Meaning: OfficeMeaningFailed, MustRelay: f.Family == "chat", Summary: joinSummary("本轮失败", label)}
	case "run.cancelled":
		return OfficeClass{Meaning: OfficeMeaningFailed, Summary: joinSummary("本轮已取消", label)}
	case "artifact.attached":
		// A file an agent produced is a result; a person's own upload is not news to them
		// (the sender filter drops it) and goes out as a plain change.
		return OfficeClass{Meaning: OfficeMeaningArtifact, MustRelay: f.UploaderType == "agent", Summary: joinSummary("有新文件 "+f.Filename, label)}
	case "issue.status_changed":
		from, to, _ := f.change("status")
		switch to {
		case "done":
			// Only a person's own hand closing release work stands in for the
			// approval (no structured approval record exists). Routing, an
			// agent, a reviewer's verdict closing it, or a Bot do not: a passed
			// code review is not a release approval.
			if !f.personsOwnHand() && IsReleaseWork(f.IssueTitle, f.IssueDescription) {
				return OfficeClass{
					Meaning: OfficeMeaningPendingRel, MustRelay: true, PendingApproval: true,
					Summary: joinSummary("系统改成完成，正式发布仍待批准", label),
				}
			}
			return OfficeClass{Meaning: OfficeMeaningTaskDone, MustRelay: true, Summary: joinSummary("任务全部完成", label)}
		case "in_review":
			return OfficeClass{Meaning: OfficeMeaningHandoff, MustRelay: true, Summary: joinSummary("已交棒等验收", label)}
		case "blocked":
			return OfficeClass{Meaning: OfficeMeaningBlocked, MustRelay: true, Summary: joinSummary("卡住了，需要人", label)}
		}
		return OfficeClass{Meaning: OfficeMeaningChange, Summary: joinSummary("状态 "+from+" → "+to, label)}
	}
	if strings.HasPrefix(f.EventType, "config.") {
		return OfficeClass{Meaning: OfficeMeaningChange, Summary: configSummary(f)}
	}
	return OfficeClass{Meaning: OfficeMeaningChange, Summary: f.EventType}
}

var configObjectNames = map[string]string{
	"workspace": "工作区设置", "member": "成员", "agent": "智能体", "squad": "小队", "squad_member": "小队成员",
	"project": "项目", "project_member": "项目成员", "project_resource": "项目资源",
	"member_bot_binding": "Bot 连接", "bot_wake": "Bot 唤醒设置", "personal_access_token": "Bot 密钥",
	"workspace_mcp_server": "MCP 服务", "agent_mcp_server": "智能体 MCP", "agent_skill": "智能体技能",
	"skill": "技能", "issue_status": "任务状态", "issue_property": "任务属性", "quick_action": "快捷动作",
	"runtime_profile": "运行环境", "resource_share": "共享", "workspace_link": "工作区连接",
	"github_installation": "GitHub 连接",
}

func configSummary(f OfficeFacts) string {
	parts := strings.Split(f.EventType, ".")
	if len(parts) < 3 {
		return f.EventType
	}
	name := configObjectNames[parts[1]]
	if name == "" {
		name = parts[1]
	}
	verb := map[string]string{"insert": "新增", "update": "修改", "delete": "删除"}[parts[2]]
	fields := make([]string, 0, len(f.Changes))
	for i, c := range f.Changes {
		if i == 4 {
			fields = append(fields, "…")
			break
		}
		fields = append(fields, c.Field)
	}
	s := name + verb
	if parts[2] == "update" && len(fields) > 0 {
		s += "：" + strings.Join(fields, "、")
	}
	return s
}

func joinSummary(head, label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return head
	}
	return head + "：" + label
}

// OfficeIssueStatusKey is the key the same business fact carries on both lanes
// (the office stream and the older activity stream), so a Bot that gets both
// shows it once. Triggers already set it for status, chat and run rows; this
// is the Go side for the activity lane.
func OfficeIssueStatusKey(issueID, from, to string, updatedAt time.Time) string {
	return "issue-status:" + issueID + ":" + from + ">" + to + ":" + strconv.FormatInt(updatedAt.UnixMicro(), 10)
}

// OfficeDeliveryBackoff is the wait before the next attempt. It never gives
// up: a person's result is retried until their Bot acknowledges it.
func OfficeDeliveryBackoff(attempts int) time.Duration {
	steps := []time.Duration{15 * time.Second, time.Minute, 3 * time.Minute, 10 * time.Minute}
	if attempts < 0 {
		attempts = 0
	}
	if attempts < len(steps) {
		return steps[attempts]
	}
	return 30 * time.Minute
}

// OfficeReceiptTimeout is how long an HTTP 200 waits for the Bot's receipt
// before the same event_id is sent again.
func OfficeReceiptTimeout(attempts int) time.Duration {
	steps := []time.Duration{2 * time.Minute, 5 * time.Minute, 15 * time.Minute}
	if attempts < len(steps) {
		return steps[attempts]
	}
	return 30 * time.Minute
}
