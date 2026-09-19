package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ReviewerKind is what the judge decided about acceptance for this issue.
type ReviewerKind string

const (
	// ReviewerSeat — an agent from the ladder should check the work.
	ReviewerSeat ReviewerKind = "seat"
	// ReviewerHuman — this acceptance needs a person, because it needs a
	// conversation rather than a check.
	ReviewerHuman ReviewerKind = "human"
	// ReviewerNone — this issue does not need a separate acceptance pass.
	//
	// This is a value, not an absence. An empty reviewer slot is re-judged on
	// every status change forever, so "no review needed" has to be written
	// down for the fill-only-empty-slots rule to ever close.
	ReviewerNone ReviewerKind = "none"
)

// Verdict is the judge's whole answer for a todo issue: a branch plus a
// confidence, and nothing else. No prose the product depends on, no action, no
// status.
type Verdict struct {
	// ExecutorTier is a tier key from the ladder.
	ExecutorTier string `json:"executor_tier"`
	// ExecutorConfidence gates the executor slot alone.
	ExecutorConfidence float64 `json:"executor_confidence"`
	// Reviewer is one of seat / human / none.
	Reviewer ReviewerKind `json:"reviewer"`
	// ReviewerTier is a tier key, meaningful only when Reviewer is seat.
	ReviewerTier string `json:"reviewer_tier"`
	// ReviewerConfidence gates the reviewer slot alone. The two slots are
	// gated separately on purpose: being sure who should do the work says
	// nothing about being sure who should check it, and one shared gate would
	// make a confident half fail with an unconfident one.
	ReviewerConfidence float64 `json:"reviewer_confidence"`
	// Reason is shown to a human in the decision comment. It is never parsed.
	Reason string `json:"reason"`
}

// Advice is the judge's answer for a blocked issue. It carries no slot values
// at all, because the blocked row of the state table writes nothing.
type Advice struct {
	// Cause is one of "tier" (the seat was not strong enough), "human" (needs
	// a person to decide), or "other".
	Cause string `json:"cause"`
	// SuggestedTier is a tier key when Cause is "tier".
	SuggestedTier string `json:"suggested_tier"`
	// Reason is one sentence of judgement and one of suggestion.
	Reason string `json:"reason"`
}

// State is the trimmed, structured view of an issue the judge is given.
//
// It is deliberately small. Seat headroom is absent because Multica already
// queues a full seat, so feeding a field that does not participate in the
// decision is noise. Credentials and full issue bodies are absent because this
// payload leaves the deployment.
type JudgeState struct {
	Title              string   `json:"title"`
	DescriptionSummary string   `json:"description_summary"`
	Labels             []string `json:"labels,omitempty"`
	Project            string   `json:"project,omitempty"`
	Repository         string   `json:"repository,omitempty"`
	Status             string   `json:"status"`
	ParentExecutor     string   `json:"parent_executor,omitempty"`
	HasChildren        bool     `json:"has_children"`
	Direction          string   `json:"direction,omitempty"`
	Candidates         []string `json:"candidate_tiers"`
}

// Judge answers the two questions Route cannot answer deterministically.
// Implementations must not write anything anywhere.
type Judge interface {
	Assign(ctx context.Context, model string, st JudgeState) (Verdict, error)
	Unblock(ctx context.Context, model string, st JudgeState) (Advice, error)
}

// TextGenerator is the slice of the server-internal LLM layer this package
// uses. pkg/llm.Client satisfies it.
type TextGenerator interface {
	GenerateJSON(ctx context.Context, model, systemPrompt, userPrompt string, temperature float64, maxCompletionTokens int64) (string, error)
}

// LLMJudge runs the judge on the server-internal LLM layer.
//
// It deliberately does not shell out to the operator's local `jev` binary:
// that binary runs on a personal machine and the server cannot reach it. What
// is reused is the discipline — a bounded branch set, one threshold living in
// one place, and no write when the answer is not clear — not the program.
type LLMJudge struct {
	Gen TextGenerator
}

// ErrJudgeUnavailable reports that no answer could be obtained. Route turns it
// into the else branch; it never becomes a partial write.
var ErrJudgeUnavailable = errors.New("routing: judge unavailable")

const assignSystemPrompt = `You route work tickets to seats on a fixed ladder of AI agents.

Answer three things and nothing else:
1. executor_tier: which ladder tier should DO this work. Choose from candidate_tiers exactly.
2. reviewer: whether this ticket needs a separate acceptance pass — "seat" (an agent checks it), "human" (acceptance needs a conversation with a person, e.g. product judgement, money, irreversible or outward-facing effects), or "none" (small, self-evident work).
3. reviewer_tier: when reviewer is "seat", which tier checks it. Choose from candidate_tiers exactly.

Large or vague tickets default to needing review. Report calibrated confidence in [0,1] separately for the executor choice and the reviewer choice; below-threshold answers are discarded rather than used, so do not inflate them.

Respond with a JSON object with keys: executor_tier, executor_confidence, reviewer, reviewer_tier, reviewer_confidence, reason. reason is one short sentence for a human reader.`

const unblockSystemPrompt = `A work ticket is blocked. Say only what is likely wrong.

cause: "tier" if the assigned seat is probably not strong enough, "human" if a person has to make a call, "other" otherwise.
suggested_tier: when cause is "tier", which tier to try instead, chosen from candidate_tiers exactly.
reason: one sentence of judgement and one of suggestion.

You are not changing anything on the ticket. Respond with a JSON object with keys: cause, suggested_tier, reason.`

// Assign asks the todo-row question. Any transport, decoding, or contract
// failure returns an error wrapping ErrJudgeUnavailable; the caller must not
// be able to mistake a broken call for a low-confidence answer.
func (j LLMJudge) Assign(ctx context.Context, model string, st JudgeState) (Verdict, error) {
	raw, err := j.ask(ctx, model, assignSystemPrompt, st)
	if err != nil {
		return Verdict{}, err
	}
	var v Verdict
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return Verdict{}, fmt.Errorf("%w: verdict was not JSON: %v", ErrJudgeUnavailable, err)
	}
	v.Reviewer = ReviewerKind(strings.ToLower(strings.TrimSpace(string(v.Reviewer))))
	switch v.Reviewer {
	case ReviewerSeat, ReviewerHuman, ReviewerNone:
	default:
		// An unrecognised branch is not a low-confidence answer, it is a
		// broken contract. Treating it as one would write a slot from a reply
		// this code does not understand.
		return Verdict{}, fmt.Errorf("%w: unknown reviewer branch %q", ErrJudgeUnavailable, v.Reviewer)
	}
	return v, nil
}

// Unblock asks the blocked-row question.
func (j LLMJudge) Unblock(ctx context.Context, model string, st JudgeState) (Advice, error) {
	raw, err := j.ask(ctx, model, unblockSystemPrompt, st)
	if err != nil {
		return Advice{}, err
	}
	var a Advice
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return Advice{}, fmt.Errorf("%w: advice was not JSON: %v", ErrJudgeUnavailable, err)
	}
	return a, nil
}

func (j LLMJudge) ask(ctx context.Context, model, system string, st JudgeState) (string, error) {
	if j.Gen == nil {
		return "", ErrJudgeUnavailable
	}
	payload, err := json.Marshal(st)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrJudgeUnavailable, err)
	}
	raw, err := j.Gen.GenerateJSON(ctx, model, system, string(payload), 0, 400)
	if err != nil {
		// Both errors are wrapped, not formatted in: the breaker classifies
		// 401/402/403/429 out of the upstream error with errors.As, and a %v
		// here would flatten it to text and silently disable that.
		return "", fmt.Errorf("%w: %w", ErrJudgeUnavailable, err)
	}
	return raw, nil
}
