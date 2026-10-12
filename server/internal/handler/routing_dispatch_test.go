package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// DENE-1834 part 4: plain-language 分工规则 are previewed by the model,
// confirmed by a person, and every confirmed save is on record.

type parsingJudge struct {
	tierJudge
	rules []routing.DispatchRule
}

func (j parsingJudge) ParseDispatch(context.Context, routing.Target, routing.DispatchParseState) ([]routing.DispatchRule, error) {
	return j.rules, nil
}

func dispatchRequest(method, path string, body any) *http.Request {
	return withURLParam(newRequest(method, "/api/workspaces/"+testWorkspaceID+path, body), "id", testWorkspaceID)
}

func TestRoutingDispatchPreviewConfirmAndHistory(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	enableDraftSuggestRouting(t, parsingJudge{
		tierJudge: tierJudge{tier: "medium", confidence: 0.95},
		rules: []routing.DispatchRule{
			{Category: "前端", Hint: "ui", Series: "Claude", Source: "前端交给 Claude"},
			{Category: "审查", Hint: "review", Series: "Gemini", Source: "审查交给 Gemini"},
		},
	})

	// Nothing saved yet: the default is in force.
	var shown routingDispatchResponse
	testutil.Call(t, testHandler.GetRoutingDispatch, dispatchRequest(http.MethodGet, "/routing/dispatch", nil)).Want(http.StatusOK).JSON(&shown)
	if !shown.IsDefault || !shown.Editable || len(shown.Policy.Rules) != len(routing.DefaultDispatchPolicy.Rules) || len(shown.Tiers) == 0 {
		t.Fatalf("initial view = %+v", shown)
	}

	// Preview writes nothing.
	var preview routing.DispatchPreview
	testutil.Call(t, testHandler.PreviewRoutingDispatch, dispatchRequest(http.MethodPost, "/routing/dispatch/preview", map[string]any{
		"text": "前端交给 Claude。审查交给 Gemini。",
	})).Want(http.StatusOK).JSON(&preview)
	if len(preview.Policy.Rules) != 2 || preview.Policy.Rules[0].Origin != "model" || preview.Policy.Version != 0 {
		t.Fatalf("preview = %+v", preview)
	}
	testutil.Call(t, testHandler.GetRoutingDispatch, dispatchRequest(http.MethodGet, "/routing/dispatch", nil)).Want(http.StatusOK).JSON(&shown)
	if !shown.IsDefault {
		t.Fatal("preview changed the stored policy")
	}
	testutil.Call(t, testHandler.PreviewRoutingDispatch, dispatchRequest(http.MethodPost, "/routing/dispatch/preview", map[string]any{"text": " "})).Want(http.StatusBadRequest)

	// Confirming the rows (one edited by hand) saves version 1 with a revision.
	rules := preview.Policy.Rules
	rules[1].Series, rules[1].Origin = "Codex", ""
	var saved routingDispatchResponse
	testutil.Call(t, testHandler.UpdateRoutingDispatch, dispatchRequest(http.MethodPut, "/routing/dispatch", map[string]any{
		"text": preview.Policy.Text, "rules": rules,
	})).Want(http.StatusOK).JSON(&saved)
	if saved.IsDefault || saved.Policy.Version != 1 || len(saved.Policy.History) != 1 {
		t.Fatalf("saved = %+v", saved.Policy)
	}
	if saved.Policy.Rules[0].Origin != "model" || saved.Policy.Rules[1].Origin != "manual" {
		t.Fatalf("origins = %s/%s", saved.Policy.Rules[0].Origin, saved.Policy.Rules[1].Origin)
	}
	rev := saved.Policy.History[0]
	if rev.By == "" || !strings.Contains(rev.RulesDiff, "~ 前端→Claude（说明或来源句改了）") || !strings.Contains(rev.RulesDiff, "− 文档沉淀 封顶中档") {
		t.Fatalf("revision = %+v", rev)
	}

	// The router now reads the saved policy.
	settings, err := testHandler.RoutingStore().Settings(context.Background(), testWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if got := settings.EffectiveDispatchPolicy(); got.Version != 1 || len(got.Rules) != 2 || got.Rules[1].Series != "Codex" {
		t.Fatalf("effective policy = %+v", got)
	}

	// An unchanged save and a bad tier are refused; an agent run may not write.
	testutil.Call(t, testHandler.UpdateRoutingDispatch, dispatchRequest(http.MethodPut, "/routing/dispatch", map[string]any{
		"text": preview.Policy.Text, "rules": rules,
	})).Want(http.StatusUnprocessableEntity)
	rules[0].MaxTier = "神"
	testutil.Call(t, testHandler.UpdateRoutingDispatch, dispatchRequest(http.MethodPut, "/routing/dispatch", map[string]any{
		"text": preview.Policy.Text, "rules": rules,
	})).Want(http.StatusUnprocessableEntity)
	asAgent := dispatchRequest(http.MethodPut, "/routing/dispatch", map[string]any{"reset": true})
	asAgent.Header.Set("X-Actor-Source", "task_token")
	testutil.Call(t, testHandler.UpdateRoutingDispatch, asAgent).Want(http.StatusForbidden)
	asAgentRead := dispatchRequest(http.MethodGet, "/routing/dispatch", nil)
	asAgentRead.Header.Set("X-Actor-Source", "task_token")
	testutil.Call(t, testHandler.GetRoutingDispatch, asAgentRead).Want(http.StatusOK).JSON(&shown)
	if shown.Editable {
		t.Fatal("an agent run was told it can edit the rules")
	}

	// Reset goes back to the default rows and stays on record.
	testutil.Call(t, testHandler.UpdateRoutingDispatch, dispatchRequest(http.MethodPut, "/routing/dispatch", map[string]any{"reset": true})).Want(http.StatusOK).JSON(&saved)
	if saved.Policy.Version != 2 || len(saved.Policy.History) != 2 || len(saved.Policy.Rules) != len(routing.DefaultDispatchPolicy.Rules) || saved.IsDefault {
		t.Fatalf("reset = %+v", saved.Policy)
	}
}
