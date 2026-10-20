package service

import (
	"strings"
	"testing"
)

func TestNormalizeActivityScopeAcceptsWholeWorkspace(t *testing.T) {
	got, err := NormalizeActivityScope([]string{"owner_visible_all", "owned"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "owned,owner_visible_all" {
		t.Fatalf("scope = %v", got)
	}
	if _, err := NormalizeActivityScope([]string{"everything"}); err == nil {
		t.Fatal("unknown scope must be refused")
	}
}

func TestDangerousBotActionCoversTheFourConfirmFirstActions(t *testing.T) {
	const ws = "11111111-1111-1111-1111-111111111111"
	cases := []struct {
		method, path, want string
	}{
		{"DELETE", "/api/workspaces/" + ws, DangerDeleteWorkspace},
		{"PATCH", "/api/workspaces/" + ws, ""},
		{"DELETE", "/api/workspaces/" + ws + "/issues/abc", ""},
		{"POST", "/api/workspaces/" + ws + "/members", DangerChangeMembers},
		{"PATCH", "/api/workspaces/" + ws + "/members/abc", DangerChangeMembers},
		{"DELETE", "/api/workspaces/" + ws + "/members/abc", DangerChangeMembers},
		{"POST", "/api/workspaces/" + ws + "/invitations", DangerChangeMembers},
		{"POST", "/api/workspaces/" + ws + "/leave", DangerChangeMembers},
		{"POST", "/api/tokens", DangerResetKeys},
		{"DELETE", "/api/tokens/abc", DangerResetKeys},
		{"POST", "/api/cli-token", DangerResetKeys},
		{"POST", "/api/workspaces/" + ws + "/bots/abc/rotate-key", DangerResetKeys},
		{"POST", "/api/cloud-billing/checkout", DangerChangeBilling},
		{"POST", "/api/cloud-subscriptions/abc/cancel", DangerChangeBilling},
		{"GET", "/api/workspaces/" + ws + "/members", ""},
		{"POST", "/api/workspaces/" + ws + "/issues", ""},
	}
	for _, c := range cases {
		if got := DangerousBotAction(c.method, c.path); got != c.want {
			t.Errorf("%s %s = %q, want %q", c.method, c.path, got, c.want)
		}
	}
}

func TestClassifyWorkspaceRoute(t *testing.T) {
	cases := map[string]string{
		"/api/workspaces/{id}/projects/{projectId}":      FamilyProject,
		"/api/workspaces/{id}/agents":                    FamilyAgent,
		"/api/workspaces/{id}/autopilots/{autopilotId}":  FamilyAutopilot,
		"/api/workspaces/{id}/skills":                    FamilySkill,
		"/api/workspaces/{id}/runtimes/{runtimeId}":      FamilyRuntime,
		"/api/workspaces/{id}/squads":                    FamilySquad,
		"/api/workspaces/{id}/members/{memberId}":        FamilyMember,
		"/api/workspaces/{id}/labels":                    FamilyRouting,
		"/api/cloud-billing/checkout":                    FamilyBilling,
		"/api/workspaces/{id}":                           FamilySettings,
		"/api/workspaces/{id}/issues/{issueId}/comments": FamilyIssue,
		"/api/something-new":                             FamilyOther,
	}
	for route, want := range cases {
		if got := ClassifyWorkspaceRoute(route); got != want {
			t.Errorf("%s = %q, want %q", route, got, want)
		}
	}
}

func TestSafeFieldNamesKeepsNamesAndDropsAnythingThatIsAValue(t *testing.T) {
	// "password" and "api_key" are names of fields (what changed), not values;
	// a name that is itself a token, or not a plain identifier, is dropped.
	got := SafeFieldNames([]string{"title", "name", "title", "password", "api_key", "bad name", "sk-abcdefghijklmnopqrstuvwx", "ghp_abcdefghijklmnopqrstuvwxyz0123", "9lead", ""})
	if strings.Join(got, ",") != "api_key,name,password,title" {
		t.Fatalf("field names = %v", got)
	}
	many := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		many = append(many, "f"+strings.Repeat("x", i%10)+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	if n := len(SafeFieldNames(many)); n > 40 {
		t.Fatalf("kept %d names", n)
	}
}
