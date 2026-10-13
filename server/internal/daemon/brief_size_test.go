package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

// openingBriefBudgetBytes caps what an agent reads before it does anything: the
// runtime brief (CLAUDE.md / AGENTS.md) plus the per-turn prompt. 12,000 bytes
// is ~4k tokens at a conservative 3 bytes/token for mixed English/Chinese text.
const openingBriefBudgetBytes = 12000

// Realistic user-authored blocks, deliberately longer than the brief's caps so
// the test measures the capped worst case rather than a short sample.
var (
	briefSizeProjectDescription = strings.Repeat("## 业务定位\n\n私有深度魔改平台工作区，负责服务端、守护进程、Web/Desktop 前端视图与核心调度逻辑的私有功能定制。\n\n## 代码主线\n\n- 仓库：GitHub 私有仓库（上游为官方源仓库）。\n- 分支策略：main 为上游镜像；功能分支切自开发主线，PR 打回主线。\n\n", 6)
	briefSizePersona            = strings.Repeat("代码基础一般的 vibe coder，主要靠 Agent 写代码、靠可见产物判断进展。汇报时先用人话说清做成了什么、卡在哪、接下来做什么，不贴代码；涉及多张票或流程时配一张 Mermaid 图，最后单列「要你做的」。\n", 12)
	briefSizeInstructions       = "You are the default executor for this workspace. Implement, verify, and deliver one issue at a time; keep diffs small and reuse existing patterns."
)

func briefSizeTask() Task {
	return Task{
		ID:                               "6f1c2b9e-0d7a-4c55-9c43-7b1f0e2a9d11",
		AgentID:                          "73f4e44b-2a52-43c4-b957-ea8b1f8b2aba",
		IssueID:                          "0b4a5d6e-7f80-4912-a3b4-c5d6e7f80912",
		WorkspaceID:                      "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d",
		Agent:                            &AgentData{ID: "73f4e44b-2a52-43c4-b957-ea8b1f8b2aba", Name: "孙悟空", Instructions: briefSizeInstructions},
		ProjectID:                        "9e8d7c6b-5a49-4382-9170-6f5e4d3c2b1a",
		ProjectTitle:                     "Multica 魔改",
		ProjectDescription:               briefSizeProjectDescription,
		ProjectResources:                 []ProjectResourceData{{ID: "r1", ResourceType: "local_directory", ResourceRef: json.RawMessage(`{"repo_key":"github.com/acme/multica","daemon_id":"01a095c6-3636-7079-a5c5-6ec659c21e53","local_path":"/Users/someone/src/multica","is_git_repo":true,"execution_mode":"worktree"}`), Label: "multica"}},
		Repos:                            []RepoData{{URL: "https://github.com/acme/ai100.git"}, {URL: "https://github.com/acme/online-tarot"}, {URL: "https://github.com/acme/agent-mono"}, {URL: "https://gitlab.example.com/coin/coin-project"}, {URL: "https://github.com/acme/image-builder"}},
		RequestingUserName:               "kunkunya",
		RequestingUserProfileDescription: briefSizePersona,
		InitiatorName:                    "kunkunya",
		InitiatorEmail:                   "kun@example.com",
	}
}

func briefSizeCtx(task Task) execenv.TaskContextForEnv {
	return execenv.TaskContextForEnv{
		IssueID:                          task.IssueID,
		TriggerCommentID:                 task.TriggerCommentID,
		TriggerThreadID:                  task.TriggerThreadID,
		AgentID:                          task.AgentID,
		AgentName:                        task.Agent.Name,
		AgentInstructions:                task.Agent.Instructions,
		Repos:                            convertReposForEnv(task.Repos),
		ProjectID:                        task.ProjectID,
		ProjectTitle:                     task.ProjectTitle,
		ProjectDescription:               task.ProjectDescription,
		ProjectResources:                 convertProjectResourcesForEnv(task.ProjectResources),
		ChatSessionID:                    task.ChatSessionID,
		RequestingUserName:               task.RequestingUserName,
		RequestingUserProfileDescription: task.RequestingUserProfileDescription,
		InitiatorName:                    task.InitiatorName,
		InitiatorEmail:                   task.InitiatorEmail,
	}
}

// TestOpeningBriefFitsBudget renders the brief plus the per-turn prompt for the
// common run shapes, with realistic (over-cap) project description and persona,
// and asserts the combined size stays within openingBriefBudgetBytes.
func TestOpeningBriefFitsBudget(t *testing.T) {
	assignment := briefSizeTask()

	comment := briefSizeTask()
	comment.TriggerCommentID = "c0ffee00-1111-4222-8333-944455556666"
	comment.TriggerThreadID = comment.TriggerCommentID
	comment.TriggerAuthorType = "member"
	comment.TriggerAuthorName = "kunkunya"
	comment.TriggerCommentContent = "这个方案我看了，方向没问题。把上面说的两个入口合成一个，手机上也要能点开，做完在这里回复我一句结论就行。"

	chat := briefSizeTask()
	chat.IssueID = ""
	chat.ChatSessionID = "5c4b3a29-1807-4f6e-9d5c-4b3a29180706"
	chat.ChatMessage = "帮我看一下今天还有哪些票卡着，按项目列一下。"

	cases := []struct {
		name string
		task Task
	}{
		{"assignment", assignment},
		{"comment", comment},
		{"chat", chat},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			brief, err := execenv.InjectRuntimeConfig(t.TempDir(), "claude", briefSizeCtx(tc.task))
			if err != nil {
				t.Fatalf("InjectRuntimeConfig: %v", err)
			}
			prompt := BuildPrompt(tc.task, "claude")
			total := len(brief) + len(prompt)
			t.Logf("%s: brief=%d bytes, prompt=%d bytes, total=%d bytes (~%d tokens at 3 bytes/token)",
				tc.name, len(brief), len(prompt), total, total/3)
			if total > openingBriefBudgetBytes {
				t.Errorf("%s: opening brief is %d bytes, over the %d-byte budget (~4k tokens)", tc.name, total, openingBriefBudgetBytes)
			}
		})
	}
}
