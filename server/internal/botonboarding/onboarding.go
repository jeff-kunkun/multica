// Package botonboarding is the one source of the "how to get started" steps a
// new external bot reads (DENE-1804). The server renders them into the setup
// text handed out with a key, `multica capabilities` prints the same steps
// above the command map, and the settings page copies the same text, so the
// three surfaces can never drift apart.
package botonboarding

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Step is one line of the onboarding: the command to run and what it gives.
type Step struct {
	Command string `json:"command"`
	Purpose string `json:"purpose"`
}

// Steps is the ordered onboarding: see the workspace first, then learn the
// commands, then fall back to --help. Edit here only.
func Steps() []Step {
	return []Step{
		{Command: "multica overview --output json", Purpose: "看工作区全景：有哪些智能体、项目、聊天、自动化、技能、小队"},
		{Command: "multica capabilities", Purpose: "看命令能力地图：每条命令能看什么、能做什么、示例，按领域分组，标出外部 bot 钥匙能不能用；--output json 给程序读"},
		{Command: "multica <命令> <子命令> --help", Purpose: "单条命令的用法；拼错子命令时错误里会提示正确的那一条"},
	}
}

// Render writes the steps as a numbered block with aligned comments. The
// heading is the caller's.
func Render() string {
	steps := Steps()
	width := 0
	for _, step := range steps {
		if n := utf8.RuneCountInString(step.Command); n > width {
			width = n
		}
	}
	var b strings.Builder
	for i, step := range steps {
		pad := strings.Repeat(" ", width-utf8.RuneCountInString(step.Command))
		fmt.Fprintf(&b, "%d. %s%s   # %s\n", i+1, step.Command, pad, step.Purpose)
	}
	return b.String()
}
