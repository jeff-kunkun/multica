package issueguard

import (
	"strings"
	"testing"
)

func TestSimilarity(t *testing.T) {
	tests := []struct {
		name, title, description, otherTitle, otherDescription string
		match                                                  bool
	}{
		{"chinese relay suffix", "马上赢修复合 test 并实测", "", "马上赢修复合 test 并实测（接力）", "", true},
		{"case and whitespace", " Fix login timeout ", "", "fix   LOGIN timeout", "", true},
		{"description keywords", "追加核对", "游戏测试环境发布失败需要定位原因", "处理异常", "游戏测试环境发布失败需要定位原因", true},
		{"shared MR despite different titles", "马上赢修复合 test 并 TEST 实测", "验收 !494", "发布核对结论", "之前已完成 !494", true},
		{"shared PR", "修复超时", "PR #42", "验收接口", "PR #42", true},
		{"qualified PR", "部署", "https://github.com/acme/app/pull/42", "合并", "https://github.com/acme/app/pull/42", true},
		{"different repositories", "部署", "https://github.com/acme/app/pull/42", "合并", "https://github.com/acme/other/pull/42", false},
		{"branch", "发布", "分支：feature/login-timeout", "核对", "branch: feature/login-timeout", true},
		{"generic branch is not evidence", "发布", "分支：test", "核对", "分支：test", false},
		{"unrelated", "修复登录超时", "接口断开", "出报销发票", "财务单据", false},
		{"empty descriptions", "abc", "", "xyz", "", false},
		{"description beyond 300", "abc", strings.Repeat("x", 301) + "共同测试内容", "xyz", strings.Repeat("y", 301) + "共同测试内容", false},
		{"MR beyond 300 still evidence", "abc", strings.Repeat("x", 301) + " !494", "xyz", "验收 !494", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score, why := Similarity(tt.title, tt.description, tt.otherTitle, tt.otherDescription)
			if (score >= SimilarThreshold) != tt.match {
				t.Fatalf("score=%.3f why=%s, want match=%v", score, why, tt.match)
			}
		})
	}
}

func TestSimilarOverrideRequiresReviewedKeysAndExplanation(t *testing.T) {
	candidates := []SimilarCandidate{
		{Identifier: "DENE-123"},
		{Identifier: "DENE-456", RecentDone: true},
	}
	tests := []struct {
		description string
		blocked     int
	}{
		{"", 2},
		{"接力 DENE-456", 1},
		{"接力 DENE-4567", 2},
		{"查重放行：", 2},
		{"查重放行：DENE-123 DENE-456", 1},
		{"查重放行：和 DENE-123 不同在另一个独立环境执行回归测试", 2},
		{"查重放行：和 DENE-123、DENE-456 不同在另一个独立环境执行回归测试", 0},
		{"quoted 查重放行：和 DENE-123、DENE-456 不同在另一个独立环境执行回归测试", 1},
	}
	for _, tt := range tests {
		if got := BlockingSimilar(tt.description, candidates); len(got) != tt.blocked {
			t.Errorf("description=%q: blocked=%d, want %d", tt.description, len(got), tt.blocked)
		}
	}
}
