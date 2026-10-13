package fastmcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

func attachmentTestHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func chunkLength(n int) *int { return &n }

func TestAttachmentReadVerifiedChunks(t *testing.T) {
	payload := bytes.Repeat([]byte("verified attachment"), 2000)
	downloads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mmc_test" || r.Header.Get("X-Workspace-ID") != testWS {
			t.Error("lost bound authorization")
		}
		if r.URL.Path == "/api/attachments/"+testID {
			_ = json.NewEncoder(w).Encode(map[string]any{"size_bytes": len(payload), "sha256": attachmentTestHash(payload), "raw_download_url": "https://untrusted.invalid/steal-token"})
			return
		}
		if r.URL.Path != "/api/attachments/"+testID+"/download" || r.URL.RawQuery != "raw=1" {
			t.Errorf("unexpected URL %s", r.URL)
			w.WriteHeader(404)
			return
		}
		downloads++
		var start, end int
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if end-start+1 > attachmentChunkMax || r.Header.Get("Accept-Encoding") != "identity" {
			t.Error("unbounded range or compression")
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.WriteHeader(206)
		_, _ = w.Write(payload[start : end+1])
	}))
	defer server.Close()
	a := &adapter{client: cli.NewAPIClient(server.URL, testWS, "mmc_test")}
	var assembled []byte
	offset := int64(0)
	for offset < int64(len(payload)) {
		out, err := a.attachmentRead(context.Background(), AttachmentReadInput{ID: testID, Offset: offset})
		if err != nil || out["code"] != nil {
			t.Fatalf("read failed %v %v", err, out)
		}
		data, err := base64.StdEncoding.DecodeString(out["data"].(string))
		if err != nil {
			t.Fatal(err)
		}
		if len(data) > attachmentChunkMax || out["chunk_sha256"] != attachmentTestHash(data) || out["sha256"] != attachmentTestHash(payload) || out["offset"] != offset {
			t.Fatal("invalid chunk contract")
		}
		assembled = append(assembled, data...)
		offset = out["next_offset"].(int64)
		if out["complete"] != (offset == int64(len(payload))) {
			t.Fatal("wrong completion")
		}
		if _, ok := out["path"]; ok {
			t.Fatal("server path leaked")
		}
	}
	if !bytes.Equal(payload, assembled) || downloads != 3 {
		t.Fatalf("assembly mismatch or downloads %d", downloads)
	}
}

func TestAttachmentReadRejectsInvalidInputsBeforeRequest(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }))
	defer server.Close()
	a := &adapter{client: cli.NewAPIClient(server.URL, testWS, "mmc_test")}
	for _, in := range []AttachmentReadInput{{ID: "../secret"}, {ID: testID, WorkspaceID: "invalid"}, {ID: testID, Offset: -1}, {ID: testID, Length: chunkLength(0)}, {ID: testID, Length: chunkLength(-1)}, {ID: testID, Length: chunkLength(16385)}} {
		out, err := a.attachmentRead(context.Background(), in)
		if err != nil || out["code"] != "invalid_arguments" {
			t.Fatalf("invalid input accepted: %v %v", out, err)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid inputs made %d requests", calls)
	}
}

func TestAttachmentReadIntegrityAndRedirectRefusal(t *testing.T) {
	payload := []byte("abcdef")
	for _, kind := range []string{"wrong range", "wrong total", "ignored range", "truncated", "oversized", "redirect", "bad checksum", "bad metadata", "too big", "offset past end", "compressed"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path == "/api/attachments/"+testID {
					hash := attachmentTestHash(payload)
					size := len(payload)
					if kind == "bad checksum" {
						hash = strings.Repeat("0", 64)
					}
					if kind == "bad metadata" {
						hash = "sha256-required"
					}
					if kind == "too big" {
						size = 100<<20 + 1
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"size_bytes": size, "sha256": hash})
					return
				}
				contentRange := "bytes 0-5/6"
				if kind == "wrong range" {
					contentRange = "bytes 1-5/6"
				}
				if kind == "wrong total" {
					contentRange = "bytes 0-5/7"
				}
				w.Header().Set("Content-Range", contentRange)
				if kind == "redirect" {
					w.Header().Set("Location", "/never-follow")
					w.WriteHeader(302)
					return
				}
				if kind == "ignored range" {
					w.WriteHeader(200)
					_, _ = w.Write(payload)
					return
				}
				if kind == "compressed" {
					w.Header().Set("Content-Encoding", "gzip")
				}
				w.WriteHeader(206)
				if kind == "truncated" {
					_, _ = w.Write(payload[:3])
					return
				}
				if kind == "oversized" {
					_, _ = w.Write(bytes.Repeat([]byte("x"), attachmentChunkMax+20))
					return
				}
				_, _ = w.Write(payload)
			}))
			defer server.Close()
			a := &adapter{client: cli.NewAPIClient(server.URL, testWS, "mmc_test")}
			in := AttachmentReadInput{ID: testID}
			if kind == "offset past end" {
				in.Offset = 6
			}
			out, err := a.attachmentRead(context.Background(), in)
			if err != nil || out["code"] == nil {
				t.Fatalf("invalid response accepted: %v %v", out, err)
			}
			if _, ok := out["data"]; ok {
				t.Fatal("unverified bytes exposed")
			}
			if calls > 2 {
				t.Fatalf("redirect or retry followed: %d", calls)
			}
		})
	}
}

func TestAttachmentReadEmptyAndShortRange(t *testing.T) {
	for _, size := range []int{0, 7} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			payload := bytes.Repeat([]byte("a"), size)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/attachments/"+testID {
					_ = json.NewEncoder(w).Encode(map[string]any{"size_bytes": size, "sha256": attachmentTestHash(payload)})
					return
				}
				if size == 0 {
					if r.Header.Get("Range") != "" {
						t.Error("range on empty file")
					}
					w.WriteHeader(200)
					return
				}
				if r.Header.Get("Range") != "bytes=5-6" {
					t.Errorf("wrong final range %s", r.Header.Get("Range"))
				}
				w.Header().Set("Content-Range", "bytes 5-6/7")
				w.WriteHeader(206)
				_, _ = w.Write(payload[5:])
			}))
			defer server.Close()
			a := &adapter{client: cli.NewAPIClient(server.URL, testWS, "mmc_test")}
			in := AttachmentReadInput{ID: testID, Length: chunkLength(3)}
			if size > 0 {
				in.Offset = 5
			}
			out, err := a.attachmentRead(context.Background(), in)
			if err != nil || out["code"] != nil || out["complete"] != true || out["next_offset"] != int64(size) {
				t.Fatalf("empty/final range failed %v %v", out, err)
			}
		})
	}
}
