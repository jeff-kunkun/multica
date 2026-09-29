package vcs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearchPullsForbiddenDoesNotEchoToken(t *testing.T) {
	const token = "ghp_TESTTOKEN_SHOULD_NOT_LEAK"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("authorization header mismatch")
		}
		if !strings.Contains(r.URL.RawQuery, "DENE-1") {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		http.Error(w, token, http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := SearchPullsByTitle(context.Background(), "github", srv.URL, token, "acme", "app", "DENE-1")
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if err != nil && strings.Contains(err.Error(), token) {
		t.Fatalf("error echoes token: %v", err)
	}
}

func TestSearchPullsEmptyIsNotForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer srv.Close()
	got, err := SearchPullsByTitle(context.Background(), "github", srv.URL, "tok", "acme", "app", "DENE-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %#v", got)
	}
}

func TestSearchPullsReadsTitleMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/search/") {
			_, _ = w.Write([]byte(`{"items":[{"number":7,"pull_request":{"url":"http://pull"}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"number":7,"title":"DENE-1 fix","state":"open","html_url":"https://github.com/acme/app/pull/7","draft":false,"merged":false,"head":{"sha":"abc","ref":"dene-1"}}`))
	}))
	defer srv.Close()
	got, err := SearchPullsByTitle(context.Background(), "github", srv.URL, "tok", "acme", "app", "DENE-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Number != 7 || got[0].SHA != "abc" || got[0].State != "open" || !strings.Contains(got[0].Title, "DENE-1") {
		t.Fatalf("got %#v", got)
	}
}
