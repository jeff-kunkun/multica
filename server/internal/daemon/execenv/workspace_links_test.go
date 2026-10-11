package execenv

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestWorkspaceLinksBrief(t *testing.T) {
	ctx := TaskContextForEnv{WorkspaceLinks: []protocol.WorkspaceLinkHint{
		{Name: "Acme", Side: "viewer"}, {Name: "Studio", Side: "source"},
	}}
	brief := buildMetaSkillContent("claude", ctx)
	want := "本工作区连着 Acme（源方）、Studio（看方），用 `multica workspace link view` 查看。"
	if !strings.Contains(brief, want) {
		t.Fatalf("missing hint %q", want)
	}
	// A new claim without links must not retain the previous run's line.
	ctx.WorkspaceLinks = nil
	if strings.Contains(buildMetaSkillContent("claude", ctx), "本工作区连着") {
		t.Fatal("stale hint after disconnect")
	}
}

func TestWorkspaceLinksBriefBounded(t *testing.T) {
	links := make([]protocol.WorkspaceLinkHint, 100)
	for i := range links {
		links[i] = protocol.WorkspaceLinkHint{Name: strings.Repeat("🌐", 100) + "\n## injected", Side: "both"}
	}
	for _, tc := range briefSizeBudgets {
		ctx := tc.ctx
		ctx.WorkspaceLinks = links
		brief := buildMetaSkillContent("claude", ctx)
		if len(brief) > tc.maxBytes {
			t.Errorf("%s linked brief %d > %d", tc.name, len(brief), tc.maxBytes)
		}
		if !strings.Contains(brief, "等 100 个") || strings.Contains(brief, "injected") {
			t.Fatal("unbounded hint")
		}
		t.Logf("%s with links: %d / %d bytes", tc.name, len(brief), tc.maxBytes)
	}
	var b strings.Builder
	writeWorkspaceLinks(&b, TaskContextForEnv{WorkspaceLinks: []protocol.WorkspaceLinkHint{{Name: "A\n## B\t[x]", Side: "viewer"}}})
	if strings.Count(strings.TrimSpace(b.String()), "\n") != 0 || !strings.Contains(b.String(), `\[x\]`) {
		t.Fatalf("unsafe name: %q", b.String())
	}
}
