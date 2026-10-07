package execenv

import (
	"os"
	"testing"
)

func TestZZDumpBriefs(t *testing.T) {
	dir := os.Getenv("BRIEF_DUMP")
	if dir == "" {
		t.Skip()
	}
	for _, tc := range briefSizeBudgets {
		b := buildMetaSkillContent("claude", tc.ctx)
		os.WriteFile(dir+"/"+tc.name+".md", []byte(b), 0o644)
		os.WriteFile(dir+"/"+tc.name+".sizes", []byte(briefSectionSizes(b)), 0o644)
	}
}
