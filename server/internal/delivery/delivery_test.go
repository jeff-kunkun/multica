package delivery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParsePullURL(t *testing.T) {
	gh, err := ParsePullURL("https://github.com/jeff-kunkun/multica/pull/392")
	if err != nil || gh.Provider != "github" || gh.Owner != "jeff-kunkun" || gh.Repo != "multica" || gh.Number != 392 {
		t.Fatalf("github = %#v %v", gh, err)
	}
	gl, err := ParsePullURL("https://gitlab.example.com/group/sub/repo/-/merge_requests/12")
	if err != nil || gl.Provider != "gitlab" || gl.Owner != "group/sub" || gl.Repo != "repo" || gl.Number != 12 {
		t.Fatalf("gitlab = %#v %v", gl, err)
	}
	if gl.ProjectPath() != "group/sub/repo" {
		t.Fatalf("project path = %s", gl.ProjectPath())
	}
	fj, err := ParsePullURL("https://git.example.com/acme/widget/pulls/3")
	if err != nil || fj.Provider != "forgejo" || fj.Number != 3 || fj.Owner != "acme" || fj.Repo != "widget" {
		t.Fatalf("forgejo = %#v %v", fj, err)
	}
	if _, err := ParsePullURL("https://github.com/o/r"); err == nil {
		t.Fatal("a repository home page is not a pull url")
	}
}

func TestValidateTokenUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := ValidateToken(context.Background(), srv.Client(), "github", srv.URL, "tok", "o", "r")
	if !Unauthorized(err) {
		t.Fatalf("err = %v", err)
	}
}
