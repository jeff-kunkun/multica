package blockwait

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// A machine wait (DENE-1212) is a close gate stop the executor can clear
// without anyone's help: checks still running or red, a conflict with the
// base, a merge the platform could not make, a PR or delivery line it could
// not read, a draft PR. Blocked means "a person has to act", so these never
// write blocked on their own. The ticket stays in_progress on a clock
// (DENE-1002's watched pause) and the patrol wakes the executor when it comes
// due. Only the same stop surviving MachineEscalateAfter rounds in a row turns
// into blocked, and then it names the person who has to look.

const (
	KeyMachineKind   = "block.machine_kind"
	KeyMachineRounds = "block.machine_rounds"

	// MachineEscalateAfter is the round on which an unresolved machine wait
	// stops waking the executor and asks a person instead.
	MachineEscalateAfter = 3
)

// Machine wait kinds. Only a repeat of the same kind counts towards
// escalation: checks going from running to red is progress, not a loop.
const (
	MachineChecksPending = "checks_pending"
	MachineChecksRed     = "checks_red"
	MachineConflict      = "conflict"
	MachineMergeFailed   = "merge_failed"
	MachineReadFailed    = "read_failed"
	MachineDraft         = "draft"
	MachineDelivery      = "delivery"
)

// MachineKeys are the escalation counter. A ticket that reaches done,
// in_review or cancelled, or leaves an escalated block, starts over.
func MachineKeys() []string {
	return []string{KeyMachineKind, KeyMachineRounds}
}

// MachinePark is what one machine wait writes: the status, the wait record,
// the sentence for the timeline and the counter.
type MachinePark struct {
	Kind      string
	Status    string
	Record    Record
	Rounds    int
	Escalated bool
	// Note is the timeline sentence; Progress is the one-line wait reason the
	// board shows under the title.
	Note     string
	Progress string
}

// ParkMachineWait decides one machine wait. meta is the issue metadata before
// this round; owner is the person an escalation names, empty when there is
// nobody to ask (the ticket then keeps waiting in progress).
func ParkMachineWait(meta map[string]any, kind, condition string, owner string, now time.Time) MachinePark {
	if now.IsZero() {
		now = time.Now()
	}
	condition = strings.TrimSpace(condition)
	if condition == "" {
		condition = "关联 PR 现在合不进去"
	}
	rounds := 1
	if MetaString(meta, KeyMachineKind) == kind {
		if prev, err := strconv.Atoi(MetaString(meta, KeyMachineRounds)); err == nil && prev > 0 {
			rounds = prev + 1
		}
	}
	park := MachinePark{Kind: kind, Rounds: rounds}
	owner = strings.TrimSpace(owner)
	if rounds >= MachineEscalateAfter && owner != "" {
		park.Status = "blocked"
		park.Escalated = true
		park.Record = Record{NeedsHuman: owner, WaitCondition: condition}
		park.Note = fmt.Sprintf("%s。平台已经连续 %d 轮叫醒执行人都没解决，需要人来看一下，票改成已阻碍。", condition, rounds)
		park.Progress = "需要人看：" + condition + fmt.Sprintf("（连续 %d 轮没解决）", rounds)
		return park
	}
	wake := now.Add(QuietAfter).UTC()
	park.Status = "in_progress"
	park.Record = Record{HasWakeAt: true, WakeAt: wake, WaitCondition: condition}
	minutes := int(QuietAfter / time.Minute)
	park.Note = fmt.Sprintf("%s。票保持进行中，%d 分钟后叫醒执行人继续，不用人管。", condition, minutes)
	if rounds > 1 {
		park.Note += fmt.Sprintf("这是同一个卡点的第 %d 轮，连续 %d 轮没解决会请负责人来看。", rounds, MachineEscalateAfter)
	}
	park.Progress = fmt.Sprintf("在等：%s，%d 分钟后叫醒执行人", condition, minutes)
	return park
}

// Pairs is the counter to store next to the wait record.
func (p MachinePark) Pairs() map[string]string {
	return map[string]string{
		KeyMachineKind:   p.Kind,
		KeyMachineRounds: strconv.Itoa(p.Rounds),
	}
}

// machineKindFor names the stop a PR snapshot is held for.
// A branch rule ("blocked") counts as checks still running while any are, so a
// required check that has not finished is not mistaken for a conflict.
func machineKindFor(pr PRSnapshot) string {
	mergeable := strings.ToLower(strings.TrimSpace(pr.Mergeable))
	switch mergeable {
	case "dirty", "behind":
		return MachineConflict
	}
	switch strings.ToLower(strings.TrimSpace(pr.Checks)) {
	case "failure", "error", "failing", "cancelled":
		return MachineChecksRed
	case "pending", "expected", "queued", "in_progress":
		return MachineChecksPending
	}
	if pr.RunningChecks > 0 {
		return MachineChecksPending
	}
	if mergeable == "blocked" {
		return MachineConflict
	}
	return MachineMergeFailed
}
