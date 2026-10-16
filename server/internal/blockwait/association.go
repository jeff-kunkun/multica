package blockwait

import (
	"fmt"
	"strings"
)

// RejectAssociationWait refuses a wait that asks the platform to notice a
// pull request on its own. A wait never produces that link; the PR does, by
// naming the issue (DENE-961).
func RejectAssociationWait(in Input, rec Record) error {
	if isAssociationWait(in.WaitCondition) || isAssociationWait(in.WaitProbe) ||
		isAssociationWait(rec.WaitCondition) || isAssociationWait(rec.WaitProbe) {
		return fmt.Errorf("不能用「等平台关联 PR」卡住。在 PR 标题、分支名或描述里写上票号，平台会自动关联")
	}
	return nil
}

func isAssociationWait(text string) bool {
	s := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(text)), " "))
	if s == "" {
		return false
	}
	for _, phrase := range []string{
		"等平台关联",
		"等待平台关联",
		"平台关联 pr",
		"等待关联 pr",
		"等关联 pr",
		"等 pr 关联",
		"等待 pr 关联",
		"wait for the platform to link",
		"waiting for the platform to link",
		"wait for the platform to associate",
		"waiting for the platform to associate",
	} {
		if strings.Contains(s, phrase) {
			return true
		}
	}
	return strings.Contains(s, "关联") && strings.Contains(s, "平台")
}
