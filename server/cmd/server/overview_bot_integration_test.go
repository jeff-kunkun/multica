package main

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"testing"

	"github.com/multica-ai/multica/server/internal/handler"
)

// Exercise the real minting routes and Auth middleware. An X-User-ID fixture
// alone would miss regressions in external bot credential authentication.
func TestOverviewExternalBotMatchesOwnerPAT(t *testing.T) {
	botResponse := authRequest(t, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/bots", map[string]any{"name": "Overview bot", "type": "grok"})
	if botResponse.StatusCode != http.StatusCreated {
		overviewRequireStatus(t, botResponse, http.StatusCreated)
	}
	var bot handler.BotKeyResponse
	readJSON(t, botResponse, &bot)
	patResponse := authRequest(t, http.MethodPost, "/api/tokens", map[string]any{"name": "Overview owner"})
	if patResponse.StatusCode != http.StatusCreated {
		overviewRequireStatus(t, patResponse, http.StatusCreated)
	}
	var pat handler.CreatePATResponse
	readJSON(t, patResponse, &pat)
	t.Cleanup(func() {
		_, err := testPool.Exec(context.Background(), `DELETE FROM personal_access_token WHERE id = $1 OR id = $2`, bot.Bot.ID, pat.ID)
		if err != nil {
			t.Errorf("clean tokens: %v", err)
		}
	})
	for _, compact := range []string{"false", "true"} {
		t.Run("compact="+compact, func(t *testing.T) {
			botSnapshot := overviewReadWithToken(t, bot.Token, testWorkspaceID, compact)
			ownerSnapshot := overviewReadWithToken(t, pat.Token, testWorkspaceID, compact)
			// Authentication updates the bot's last-used timestamp. Everything else,
			// including all visible rows/counts and omitted sections, must agree.
			overviewRemoveLastUsed(botSnapshot)
			overviewRemoveLastUsed(ownerSnapshot)
			if !reflect.DeepEqual(botSnapshot, ownerSnapshot) {
				t.Fatal("bot and owner PAT overview snapshots differ")
			}
			for _, section := range []string{"agents", "projects", "chats", "automations", "skills", "squads", "runtimes", "workspace_links", "external_connections"} {
				if _, ok := botSnapshot[section].([]any); !ok {
					t.Errorf("missing array %s", section)
				}
			}
		})
	}
	// A valid bot key must still fail against a workspace its owner cannot see.
	var otherWorkspace string
	if err := testPool.QueryRow(context.Background(), `INSERT INTO workspace(name,slug) VALUES ('Overview hidden', 'overview-hidden-' || gen_random_uuid()::text) RETURNING id`).Scan(&otherWorkspace); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id=$1`, otherWorkspace)
		if err != nil {
			t.Error(err)
		}
	})
	for _, token := range []string{bot.Token, pat.Token} {
		response := overviewTokenRequest(t, token, otherWorkspace, "false")
		overviewRequireStatus(t, response, http.StatusNotFound)
	}
	// Stopping an actual bot invalidates its key even after successful reads.
	stopped := authRequest(t, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/bots/"+bot.Bot.ID+"/stop", nil)
	overviewRequireStatus(t, stopped, http.StatusOK)
	overviewRequireStatus(t, overviewTokenRequest(t, bot.Token, testWorkspaceID, "false"), http.StatusUnauthorized)
}

func overviewTokenRequest(t *testing.T, token, workspaceID, compact string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, testServer.URL+"/api/overview?workspace_id="+workspaceID+"&compact="+compact, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
func overviewReadWithToken(t *testing.T, token, workspaceID, compact string) map[string]any {
	t.Helper()
	response := overviewTokenRequest(t, token, workspaceID, compact)
	if response.StatusCode != http.StatusOK {
		overviewRequireStatus(t, response, http.StatusOK)
	}
	var snapshot map[string]any
	readJSON(t, response, &snapshot)
	return snapshot
}
func overviewRequireStatus(t *testing.T, response *http.Response, want int) {
	t.Helper()
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != want {
		t.Fatalf("HTTP %d, want %d: %s", response.StatusCode, want, body)
	}
}
func overviewRemoveLastUsed(value any) {
	switch v := value.(type) {
	case map[string]any:
		delete(v, "last_used_at")
		for _, nested := range v {
			overviewRemoveLastUsed(nested)
		}
	case []any:
		for _, nested := range v {
			overviewRemoveLastUsed(nested)
		}
	}
}
