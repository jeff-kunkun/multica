package mirror

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HTTPRemote writes to a Multica server's public API as one of its members.
type HTTPRemote struct {
	baseURL   string
	token     string
	workspace string
	client    *http.Client
}

func NewHTTPRemote(baseURL, token, workspace string) *HTTPRemote {
	return &HTTPRemote{
		baseURL:   baseURL,
		token:     token,
		workspace: workspace,
		client:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (r *HTTPRemote) Create(ctx context.Context, path string, body map[string]any) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	if err := r.do(ctx, http.MethodPost, path, body, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", fmt.Errorf("POST %s: response has no id", path)
	}
	return out.ID, nil
}

func (r *HTTPRemote) Update(ctx context.Context, path string, body map[string]any) error {
	return r.do(ctx, http.MethodPut, path, body, nil)
}

func (r *HTTPRemote) Delete(ctx context.Context, path string) error {
	return r.do(ctx, http.MethodDelete, path, nil, nil)
}

func (r *HTTPRemote) do(ctx context.Context, method, path string, body map[string]any, out any) error {
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.baseURL+path, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("X-Workspace-ID", r.workspace)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrRemoteNotFound
	}
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, bytes.TrimSpace(msg))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
