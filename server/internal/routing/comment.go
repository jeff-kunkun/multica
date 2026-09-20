package routing

import (
	"fmt"
	"strconv"
	"strings"
)

// The routing comment IS the decision log. There is no second log file: a
// separate one would have to be found, would not be readable by the person the
// decision affects, and would drift from what the ticket actually shows.

const changeSlotFooter = "改右侧任一格即可，改完那一格不会被自动改回。"

func pct(v float64) string {
	return strconv.FormatFloat(v*100, 'f', 0, 64) + "%"
}

func directionLine(issue Issue, match DirectionMatch) string {
	switch {
	case issue.ProjectName == "":
		return "- **方向**：未知（本票没有所属 project），从通用档位里选"
	case match.Invalid != "":
		return fmt.Sprintf("- **方向**：未知（对照表把 project「%s」写成了「%s」，但没有这个方向），从通用档位里选",
			issue.ProjectName, match.Invalid)
	case !match.Known:
		return fmt.Sprintf("- **方向**：未知（project「%s」不在对照表里），从通用档位里选", issue.ProjectName)
	case match.Direction == "":
		return fmt.Sprintf("- **方向**：%s（对照表把 project「%s」归为通用），从通用档位里选", GenericDirection, issue.ProjectName)
	}
	return fmt.Sprintf("- **方向**：%s（来自 project「%s」）", match.Direction, issue.ProjectName)
}

// assignmentComment is the todo-row decision comment: what went into each
// slot, where the direction came from, the confidence against the threshold,
// and the verdict.
func (r *Router) assignmentComment(
	issue Issue,
	match DirectionMatch,
	candidates []Seat,
	v Verdict,
	threshold float64,
	executor *Seat,
	executorSource string,
	reviewerName string,
	reviewerFallback bool,
	needExecutor, needReviewer, hasReviewerSlot bool,
	stillUnassigned bool,
) string {
	var b strings.Builder
	b.WriteString("## 自动选派\n\n")

	// Executor slot.
	switch {
	case !needExecutor:
		b.WriteString("- **执行席**：由你指定，未改动\n")
	case executor != nil && executorSource == pickLabel:
		b.WriteString(fmt.Sprintf("- **执行席**：%s（%s档，按票上的「%s」标签选的，没问模型）→ 已派出，run 已启动\n",
			executor.Name, executor.TierLabel, executor.TierLabel))
	case executor != nil && executorSource == pickFallback:
		b.WriteString(fmt.Sprintf("- **执行席**：%s（%s档，**兜底档**——裁决置信度 %s 低于阈值 %s，或它点的档位这里没有席位）→ 仍然派出，run 已启动。觉得档位不对直接改，改了路由不会再碰\n",
			executor.Name, executor.TierLabel, pct(v.ExecutorConfidence), pct(threshold)))
	case executor != nil:
		b.WriteString(fmt.Sprintf("- **执行席**：%s（%s档，置信度 %s ≥ 阈值 %s）→ 已派出，run 已启动\n",
			executor.Name, executor.TierLabel, pct(v.ExecutorConfidence), pct(threshold)))
	default:
		b.WriteString("- **执行席**：⚠️ 未填——这一格在本次裁决与写入之间被别人占了\n")
	}

	// Reviewer slot.
	switch {
	case !hasReviewerSlot:
		b.WriteString("- **验收席**：本工作区的「验收席」属性不可用（已归档，或同名属性是别的类型），这一格没写\n")
	case !needReviewer:
		b.WriteString(fmt.Sprintf("- **验收席**：已有值「%s」，未改动\n", issue.Reviewer))
	case reviewerFallback && reviewerName == OptionHuman:
		b.WriteString(fmt.Sprintf("- **验收席**：交给人（**兜底**——裁决置信度 %s 低于阈值 %s，执行席已在最强档，没有更高一档可验）\n",
			pct(v.ReviewerConfidence), pct(threshold)))
	case reviewerFallback && reviewerName != "":
		b.WriteString(fmt.Sprintf("- **验收席**：%s（**兜底**——裁决置信度 %s 低于阈值 %s，按「比执行席高一档」选的）\n",
			reviewerName, pct(v.ReviewerConfidence), pct(threshold)))
	case reviewerName == OptionNoReview:
		b.WriteString(fmt.Sprintf("- **验收席**：本票不需要验收（置信度 %s）。要人复核就自己填一个\n", pct(v.ReviewerConfidence)))
	case reviewerName == OptionHuman:
		b.WriteString(fmt.Sprintf("- **验收席**：交给人（置信度 %s）——判为这次验收需要沟通\n", pct(v.ReviewerConfidence)))
	case reviewerName != "":
		b.WriteString(fmt.Sprintf("- **验收席**：%s（置信度 %s ≥ 阈值 %s）\n",
			reviewerName, pct(v.ReviewerConfidence), pct(threshold)))
	default:
		b.WriteString("- **验收席**：⚠️ 未填——这一格在本次裁决与写入之间被别人占了，或「验收席」属性里没有这个选项\n")
	}

	b.WriteString(directionLine(issue, match))
	b.WriteString("\n")
	b.WriteString("- **候选**：" + seatNames(candidates) + "\n")
	if strings.TrimSpace(v.Reason) != "" {
		b.WriteString("- **判断**：" + strings.TrimSpace(v.Reason) + "\n")
	}
	b.WriteString("\n")

	if stillUnassigned {
		b.WriteString("⚠️ 执行席没派出去，这张票会一直躺在待办，所以 @ 你一次。\n\n")
	}
	b.WriteString(changeSlotFooter)
	b.WriteString("\n\n**路由没有改过状态** —— 状态是事实，只有干活的席位知道。")
	return b.String()
}

// handoffComment is the in-review-row comment.
func (r *Router) handoffComment(issue Issue, to string, toHuman bool, target Member) string {
	var b strings.Builder
	b.WriteString("## 交接\n\n")
	b.WriteString("这张票进入了待验收。\n\n")
	from := "原执行席"
	if issue.AssigneeType == "" {
		from = "无人"
	}
	b.WriteString(fmt.Sprintf("- 已从 %s 改派给 **%s**\n", from, to))
	if toHuman {
		b.WriteString("- 验收席填的是人，所以 @ 你，不会有 agent 被叫醒\n")
	} else {
		b.WriteString("- 指派本身就是叫醒，这个席位的 run 已启动\n")
	}
	b.WriteString("\n验收席只做检查、合并、关票，不重做这张票的活；认为要返工就把票改回执行席并说明原因。\n")
	b.WriteString("\n**路由没有改过状态。**")
	return b.String()
}

// adviceComment is the blocked-row comment. It must state, in so many words,
// that nothing was changed — an advisory comment that looks like an action is
// worse than none.
func (r *Router) adviceComment(issue Issue, a Advice, candidates []Seat) string {
	var b strings.Builder
	b.WriteString("## 建议\n\n")
	switch a.Cause {
	case "tier":
		seatName := a.SuggestedTier
		if s, ok := SeatByTier(candidates, a.SuggestedTier); ok {
			seatName = fmt.Sprintf("%s（%s档）", s.Name, s.TierLabel)
		}
		b.WriteString("判断：像是档位不够导致的卡点。\n\n")
		b.WriteString("建议：换 " + seatName + " 再试。\n")
	case "human":
		b.WriteString("判断：这个卡点需要人来拍板。\n\n")
	default:
		b.WriteString("判断：卡点不像档位问题，也不像单纯等人。\n\n")
	}
	if strings.TrimSpace(a.Reason) != "" {
		b.WriteString("\n" + strings.TrimSpace(a.Reason) + "\n")
	}
	b.WriteString("\n**没有改动任何值** —— 执行席、验收席、状态都保持原样。这条只是建议，@ 你一次是因为票卡住了就没人会动它。")
	return b.String()
}

// unavailableComment is posted once when a configured model cannot be reached.
// After it, the breaker keeps the workspace quiet until the cooldown elapses.
func (r *Router) unavailableComment(issue Issue) string {
	return "## 路由未生效\n\n" +
		"路由模型这次没能给出判断，所以这张票**一个值都没有被写入**，执行席和验收席都保持原样。\n\n" +
		"原因只在设置的「路由」一节里显示，票上不会反复留言：连续失败后路由会进入冷却，冷却期内不再发请求。"
}

func seatNames(seats []Seat) string {
	if len(seats) == 0 {
		return "（空）"
	}
	names := make([]string, 0, len(seats))
	for _, s := range seats {
		names = append(names, fmt.Sprintf("%s(%s)", s.Name, s.TierLabel))
	}
	return strings.Join(names, " · ")
}
