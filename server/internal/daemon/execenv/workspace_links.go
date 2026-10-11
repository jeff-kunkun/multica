package execenv

import (
	"fmt"
	"strings"
)

// Keep workspace discovery to one bounded line, regardless of workspace count
// or name length. Side is local; the label describes the peer's role.
func writeWorkspaceLinks(b *strings.Builder, ctx TaskContextForEnv) {
	if len(ctx.WorkspaceLinks) == 0 {
		return
	}
	names := make([]string, 0, 2)
	truncated := false
	for _, link := range ctx.WorkspaceLinks {
		if len(names) == 2 {
			truncated = true
			break
		}
		name := []rune(strings.Join(strings.Fields(link.Name), " "))
		if len(name) > 10 {
			name = append(name[:10], '…')
			truncated = true
		}
		role := "源方"
		if link.Side == "source" {
			role = "看方"
		} else if link.Side == "both" {
			role = "源方/看方"
		}
		names = append(names, sanitizeNameForBriefMarkdown(string(name))+"（"+role+"）")
	}
	fmt.Fprintf(b, "本工作区连着 %s", strings.Join(names, "、"))
	if truncated {
		fmt.Fprintf(b, " 等 %d 个", len(ctx.WorkspaceLinks))
	}
	b.WriteString("，用 `multica workspace link view` 查看。\n\n")
}
