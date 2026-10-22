package titling

import (
	"strings"
	"testing"
)

func TestIssueTitleBriefSection(t *testing.T) {
	for _, want := range []string{
		"## Title Style",
		"`{Project}: {what}`",
		"issue → 任务",
		"agent → 智能体",
		"autopilot → 自动化",
		"{Project} · {topic}",
		"A title the user dictated stays",
	} {
		if !strings.Contains(IssueTitleBriefSection, want) {
			t.Errorf("issue title section missing %q", want)
		}
	}
}
