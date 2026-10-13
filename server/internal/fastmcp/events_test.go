package fastmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/multica-ai/multica/server/internal/cli"
)

func TestOfficeEventToolsPathsAndReceipts(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("X-Workspace-ID") != testWS {
			t.Error("missing workspace")
		}
		prefix := "/api/workspaces/" + testWS
		switch calls {
		case 1:
			if r.URL.Path != prefix+"/office-events" || r.URL.Query().Get("after") != "42" || r.URL.Query().Get("limit") != "50" || r.URL.Query().Get("family") != "issue&extra=true" {
				t.Errorf("list path %s", r.URL)
			}
			fmt.Fprint(w, `{"events":[],"next_cursor":43,"schema":"office_event.v1"}`)
		case 2:
			if r.URL.Path != prefix+"/office-events/"+testID {
				t.Error(r.URL)
			}
			fmt.Fprint(w, `{"event_id":"`+testID+`","deliveries":[]}`)
		case 3:
			if r.URL.Path != prefix+"/bots/"+testID+"/office-deliveries" || r.URL.Query().Get("before") != "7" {
				t.Error(r.URL)
			}
			fmt.Fprint(w, `{"deliveries":[],"next_cursor":0}`)
		case 4:
			if r.Method != "POST" || r.URL.Path != prefix+"/bots/"+testID+"/office-events/"+testID+"/receipt" {
				t.Error(r.URL)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["stage"] != "relayed" || body["message_ref"] != "external-message" || body["channel"] != "group" || body["target"] != "group-reference" {
				t.Error(body)
			}
			artifacts, ok := body["artifacts"].([]any)
			if !ok || len(artifacts) != 1 || artifacts[0].(map[string]any)["ref"] != "external-file" {
				t.Error(body)
			}
			if _, ok := body["workspace_id"]; ok {
				t.Error("routing field leaked into body")
			}
			fmt.Fprint(w, `{"status":"relayed"}`)
		default:
			t.Error("unexpected request")
		}
	}))
	defer srv.Close()
	a := &adapter{client: cli.NewAPIClient(srv.URL, testWS, "fixture")}
	ctx := context.Background()
	out, err := a.events(ctx, EventsInput{After: 42, Family: "issue&extra=true"})
	if err != nil || out["next_cursor"] != json.Number("43") {
		t.Fatalf("%v %v", out, err)
	}
	if _, err = a.event(ctx, EventInput{EventID: testID}); err != nil {
		t.Fatal(err)
	}
	if _, err = a.deliveries(ctx, DeliveriesInput{BotID: testID, Before: 7}); err != nil {
		t.Fatal(err)
	}
	out, err = a.receipt(ctx, ReceiptInput{BotID: testID, EventID: testID, Stage: "relayed", Channel: "group", Target: "group-reference", MessageRef: "external-message", Artifacts: []ArtifactReceipt{{ID: testID, Status: "sent", Ref: "external-file"}}})
	if err != nil || out["status"] != "relayed" || calls != 4 {
		t.Fatalf("%v %v calls=%d", out, err, calls)
	}
}

func TestOfficeEventToolsRejectInvalidInputWithoutRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("invalid input reached API") }))
	defer srv.Close()
	a := &adapter{client: cli.NewAPIClient(srv.URL, testWS, "fixture")}
	ctx := context.Background()
	cases := []func() (map[string]any, error){
		func() (map[string]any, error) { return a.events(ctx, EventsInput{WorkspaceID: "../x"}) },
		func() (map[string]any, error) { return a.events(ctx, EventsInput{After: -1}) },
		func() (map[string]any, error) { return a.events(ctx, EventsInput{Limit: 201}) },
		func() (map[string]any, error) { return a.events(ctx, EventsInput{IssueID: "../x"}) },
		func() (map[string]any, error) { return a.event(ctx, EventInput{EventID: "../x"}) },
		func() (map[string]any, error) { return a.deliveries(ctx, DeliveriesInput{BotID: "../x"}) },
		func() (map[string]any, error) { return a.deliveries(ctx, DeliveriesInput{BotID: testID, Before: -1}) },
		func() (map[string]any, error) {
			return a.receipt(ctx, ReceiptInput{BotID: testID, EventID: testID, Stage: "answered"})
		},
	}
	for i, run := range cases {
		out, err := run()
		if err != nil || out["code"] != "invalid_arguments" {
			t.Errorf("case %d: %v %v", i, out, err)
		}
	}
}

func TestOfficeReceiptServerValidationAndAmbiguousWrite(t *testing.T) {
	for _, status := range []int{400, 403, 409, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":"sensitive-detail"}`)
			}))
			defer srv.Close()
			a := &adapter{client: cli.NewAPIClient(srv.URL, testWS, "fixture")}
			out, err := a.receipt(context.Background(), ReceiptInput{BotID: testID, EventID: testID, Stage: "relayed"})
			if err != nil || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
			data, _ := json.Marshal(out)
			if strings.Contains(string(data), "sensitive-detail") {
				t.Fatal("raw API error leaked")
			}
			if status == 500 && (out["code"] != "ambiguous_write" || out["check_tool"] != "event_get") {
				t.Error(out)
			}
			if status != 500 && out["code"] == "ambiguous_write" {
				t.Error(out)
			}
		})
	}
}

func TestOfficeReceiptSchema(t *testing.T) {
	schema, err := jsonschema.For[ReceiptInput](nil)
	if err != nil {
		t.Fatal(err)
	}
	constrainEventSchema("events_receipt", schema)
	if len(schema.Properties["stage"].Enum) != 4 {
		t.Fatal("stage schema unconstrained")
	}
	raw, _ := json.Marshal(schema)
	if !strings.Contains(string(raw), `"enum":["sent","failed","skipped"]`) {
		t.Fatalf("artifact schema unconstrained: %s", raw)
	}
}
