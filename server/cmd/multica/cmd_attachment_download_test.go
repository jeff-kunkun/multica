package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAttachmentDownloadExactBytes(t *testing.T) {
	for _, f := range []struct{ name, contentType, body string }{
		{"report.html", "text/html; charset=utf-8", "<!doctype html><p>原始字节</p>"},
		{"report.pdf", "application/pdf", "%PDF-1.7\x00\xff\r\n"},
		{"shot.png", "image/png", "\x89PNG\r\n\x1a\n\x00\xff"},
		{"report.txt", "text/plain; charset=utf-8", "文本\r\n"},
	} {
		t.Run(f.name, func(t *testing.T) {
			body := bytes.Repeat([]byte(f.body), 150000)
			var downloads int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/attachments/fixture" {
					json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "filename": f.name, "download_url": "/preview", "raw_download_url": "/api/attachments/fixture/download?raw=1", "size_bytes": len(body), "sha256": fmt.Sprintf("%x", sha256.Sum256(body)), "content_type": f.contentType})
					return
				}
				if r.URL.Path != "/api/attachments/fixture/download" || r.URL.Query().Get("raw") != "1" {
					t.Errorf("unexpected URL %s", r.URL)
					return
				}
				downloads++
				if r.Header.Get("Accept-Encoding") != "identity" || !strings.Contains(r.Header.Get("Cache-Control"), "no-transform") {
					t.Error("raw download must request identity/no-transform")
				}
				w.Header().Set("Content-Type", f.contentType)
				w.Write(body)
			}))
			defer srv.Close()
			setCLITestServerEnv(t, srv.URL)
			t.Setenv("MULTICA_TOKEN", "mat_test-token")
			cmd := newAttachmentDownloadTestCmd()
			dest := filepath.Join(t.TempDir(), "nested", f.name)
			cmd.Flags().Set("output-file", dest)
			_, err := captureStdout(t, func() error { return runAttachmentDownload(cmd, []string{"fixture"}) })
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(dest)
			if err != nil || !bytes.Equal(got, body) || downloads != 1 {
				t.Fatalf("exact bytes=%v downloads=%d error=%v", bytes.Equal(got, body), downloads, err)
			}
		})
	}
}

func TestAttachmentDownloadRawRetry(t *testing.T) {
	for _, failRetry := range []bool{false, true} {
		t.Run(fmt.Sprint(failRetry), func(t *testing.T) {
			good := []byte("<!doctype html><p>original</p>")
			var downloads int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/attachments/fixture" {
					json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "filename": "report.html", "download_url": "/preview", "size_bytes": len(good), "sha256": fmt.Sprintf("%x", sha256.Sum256(good))})
					return
				}
				downloads++
				if r.URL.Path == "/preview" || failRetry {
					w.Write([]byte("transformed"))
					return
				}
				if r.URL.Query().Get("raw") != "1" {
					t.Error("retry must use raw storage")
				}
				w.Write(good)
			}))
			defer srv.Close()
			setCLITestServerEnv(t, srv.URL)
			t.Setenv("MULTICA_TOKEN", "mat_test-token")
			cmd := newAttachmentDownloadTestCmd()
			dir := t.TempDir()
			dest := filepath.Join(dir, "report.html")
			os.WriteFile(dest, []byte("existing good file"), 0600)
			cmd.Flags().Set("output-dir", dir)
			stderr := captureStderr(t)
			_, err := captureStdout(t, func() error { return runAttachmentDownload(cmd, []string{"fixture"}) })
			warning := stderr.read()
			if downloads != 2 || !strings.Contains(warning, "retrying once") {
				t.Fatalf("downloads=%d stderr=%s", downloads, warning)
			}
			got, _ := os.ReadFile(dest)
			if failRetry {
				if err == nil || !strings.Contains(err.Error(), "expected sha256=") || !strings.Contains(err.Error(), "actual sha256=") || !strings.Contains(err.Error(), "authenticated raw storage") {
					t.Fatalf("incomplete diagnostic: %v", err)
				}
				if string(got) != "existing good file" {
					t.Fatal("corrupt download replaced good file")
				}
				entries, _ := os.ReadDir(dir)
				if len(entries) != 1 {
					t.Fatal("unverified temporary file retained")
				}
			} else if err != nil || !bytes.Equal(got, good) {
				t.Fatalf("retry failed: %v", err)
			}
		})
	}
}

func TestAttachmentDownloadBatch(t *testing.T) {
	for _, selector := range []string{"comment", "issue", "issue-comment", "partial"} {
		t.Run(selector, func(t *testing.T) {
			body := []byte("bytes")
			var gotIDs []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/issues/DENE-1":
					json.NewEncoder(w).Encode(map[string]any{"id": "issue", "identifier": "DENE-1"})
				case "/api/comments/comment/attachments", "/api/issues/issue/attachments":
					json.NewEncoder(w).Encode([]map[string]any{{"id": "one", "filename": "../report.html", "comment_id": "comment"}, {"id": "two", "filename": "report.html", "comment_id": "comment"}, {"id": "three", "filename": "report-one.html", "comment_id": "other"}})
				case "/api/attachments/one", "/api/attachments/two", "/api/attachments/three":
					id := strings.TrimPrefix(r.URL.Path, "/api/attachments/")
					gotIDs = append(gotIDs, id)
					digest := fmt.Sprintf("%x", sha256.Sum256(body))
					if selector == "partial" && id == "two" {
						digest = strings.Repeat("0", 64)
					}
					json.NewEncoder(w).Encode(map[string]any{"id": id, "filename": "report.html", "download_url": "/bytes", "size_bytes": len(body), "sha256": digest})
				default:
					w.Write(body)
				}
			}))
			defer srv.Close()
			setCLITestServerEnv(t, srv.URL)
			t.Setenv("MULTICA_TOKEN", "mat_test-token")
			cmd := newAttachmentDownloadTestCmd()
			dir := t.TempDir()
			cmd.Flags().Set("output-dir", dir)
			if selector == "issue" || selector == "issue-comment" {
				cmd.Flags().Set("issue", "DENE-1")
			}
			if selector != "issue" {
				cmd.Flags().Set("comment", "comment")
			}
			out, err := captureStdout(t, func() error { return runAttachmentDownload(cmd, nil) })
			if (err != nil) != (selector == "partial") {
				t.Fatalf("err=%v", err)
			}
			var result struct {
				Files    []attachmentDownloadResult `json:"files"`
				Failures []map[string]string        `json:"failures"`
			}
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatal(err)
			}
			want := 3
			if selector == "issue-comment" {
				want = 2
			}
			if selector == "partial" {
				want = 2
				if len(result.Failures) != 1 {
					t.Fatal("failure not reported")
				}
			}
			if len(result.Files) != want {
				t.Fatalf("files=%d want=%d; %s", len(result.Files), want, out)
			}
			names := map[string]bool{}
			for _, file := range result.Files {
				if names[file.Path] {
					t.Fatal("batch overwrote duplicate filename")
				}
				names[file.Path] = true
				data, err := os.ReadFile(file.Path)
				if err != nil || !bytes.Equal(data, body) {
					t.Fatalf("batch file: %v", err)
				}
			}
			if selector == "issue-comment" && strings.Join(gotIDs, ",") != "one,two" {
				t.Fatalf("comment filter: %v", gotIDs)
			}
		})
	}
}

func TestAttachmentDownloadSelectors(t *testing.T) {
	for _, flags := range []map[string]string{{}, {"comment": "c", "output-file": "report"}, {"output": "./attachments"}} {
		cmd := newAttachmentDownloadTestCmd()
		for key, value := range flags {
			cmd.Flags().Set(key, value)
		}
		if err := runAttachmentDownload(cmd, nil); err == nil {
			t.Fatalf("accepted invalid flags: %v", flags)
		}
	}
	cmd := newAttachmentDownloadTestCmd()
	cmd.Flags().Set("comment", "c")
	if err := runAttachmentDownload(cmd, []string{"id"}); err == nil {
		t.Fatal("accepted ID and comment together")
	}
}

// DENE-1946: a CDN that rewrites a preview (injected script, +367 bytes on a
// >1 MiB HTML) must not cost the user the file. The registered size/sha256
// describe the stored object, so the raw retry delivers it intact.
func TestAttachmentDownloadLargeCDNRewriteFallsBackToRaw(t *testing.T) {
	good := bytes.Repeat([]byte("<!doctype html><p>字节</p>"), 60000)
	rewritten := append(append([]byte{}, good...), bytes.Repeat([]byte("x"), 367)...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/attachments/fixture":
			json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "filename": "large.html", "download_url": "/preview", "size_bytes": len(good), "sha256": fmt.Sprintf("%x", sha256.Sum256(good))})
		case r.URL.Path == "/preview":
			w.Write(rewritten)
		case r.URL.Query().Get("raw") == "1":
			w.Write(good)
		default:
			t.Errorf("unexpected URL %s", r.URL)
		}
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	cmd := newAttachmentDownloadTestCmd()
	dest := filepath.Join(t.TempDir(), "large.html")
	cmd.Flags().Set("output-file", dest)
	stderr := captureStderr(t)
	if _, err := captureStdout(t, func() error { return runAttachmentDownload(cmd, []string{"fixture"}) }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.read(), "retrying once from raw storage") {
		t.Fatal("expected the raw retry warning")
	}
	if got, _ := os.ReadFile(dest); !bytes.Equal(got, good) {
		t.Fatalf("saved %d bytes, want %d exact bytes", len(got), len(good))
	}
}
