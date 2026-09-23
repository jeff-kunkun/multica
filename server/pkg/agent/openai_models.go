package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// openAIModelsTimeout bounds one GET {base}/models probe. The call is a
// localhost or LAN round trip, not a CLI spawn, so it stays well inside the
// server's model-list running window. A tunnel that never answers fails here
// and the picker reports the miss instead of waiting out the whole window.
const openAIModelsTimeout = 10 * time.Second

// openAIModelsBodyLimit caps a catalog body. Big enough for a real listing,
// small enough that a misbehaving endpoint cannot pin memory.
const openAIModelsBodyLimit = 2 << 20

// openAIModelsHTTP is the client for OpenAI-compatible catalog probes.
// Redirects are refused: following one would replay the Authorization header
// at whatever host answered, and a model list does not need a redirect.
var openAIModelsHTTP = &http.Client{
	Timeout: openAIModelsTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// discoverOpenAICompatibleModels asks an OpenAI-compatible endpoint for its
// model ids. baseURL is the API root the runtime actually calls (it already
// includes /v1 when the runtime's config does). apiKey is sent only as a
// Bearer header on this process; it is never written into the URL, the error
// text, or a log line. defaultModel, when it matches a returned id, is marked
// Default so the picker can badge it.
//
// A transport failure, a non-2xx status, or a body that is not a model list
// is an error. That error is how the picker says the list is temporarily
// unavailable. A parsed list that happens to be empty is a confirmed empty
// catalog (nil error): the endpoint answered and has nothing to offer.
func discoverOpenAICompatibleModels(ctx context.Context, baseURL, apiKey, defaultModel string) ([]Model, error) {
	endpoint, host, err := openAIModelsEndpoint(baseURL)
	if err != nil {
		return nil, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, openAIModelsTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errModelsListUnavailable("无法发起模型列表请求")
	}
	if key := strings.TrimSpace(apiKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := openAIModelsHTTP.Do(req)
	if err != nil {
		return nil, errModelsListUnavailable("连不上 " + host)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, openAIModelsBodyLimit))
	if err != nil {
		return nil, errModelsListUnavailable("读不到 " + host + " 的响应")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errModelsListUnavailable(fmt.Sprintf("端点返回了 HTTP %d", resp.StatusCode))
	}
	models, empty, err := parseOpenAIModelList(body, strings.TrimSpace(defaultModel))
	if err != nil {
		return nil, errModelsListUnavailable("端点没有返回模型清单")
	}
	if empty {
		return nil, nil
	}
	return models, nil
}

// openAIModelsEndpoint turns a configured API root into {root}/models and
// returns the host to name in errors. Userinfo and the query string are
// dropped so a key embedded in the configured URL cannot leave this process
// through the request target or an error string.
func openAIModelsEndpoint(baseURL string) (endpoint, host string, err error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", "", errModelsListUnavailable("没有读到 OpenAI 兼容端点")
	}
	host = parsed.Host
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(path, "/models") {
		if path == "" {
			path = "/models"
		} else {
			path += "/models"
		}
	}
	parsed.Path = path
	return parsed.String(), host, nil
}

type openAIModelEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

// parseOpenAIModelList reads an OpenAI-compatible listing.
//
// empty is true only when the body is a recognized list and that list has no
// ids — `{"data":[]}`, `{"models":[]}`, or `[]`. A JSON object that merely
// lacks those fields is not a list, and comes back as an error so the caller
// does not treat "we could not read this" as "the endpoint has no models".
func parseOpenAIModelList(body []byte, defaultModel string) (models []Model, empty bool, err error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, false, fmt.Errorf("empty body")
	}
	if trimmed[0] == '[' {
		var entries []openAIModelEntry
		if err := json.Unmarshal(trimmed, &entries); err != nil {
			return nil, false, err
		}
		return modelsFromOpenAIEntries(entries, defaultModel)
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Models json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(trimmed, &envelope); err != nil {
		return nil, false, err
	}
	raw := envelope.Data
	if len(raw) == 0 {
		raw = envelope.Models
	}
	if len(raw) == 0 {
		return nil, false, fmt.Errorf("no model list")
	}
	var entries []openAIModelEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, false, err
	}
	return modelsFromOpenAIEntries(entries, defaultModel)
}

func modelsFromOpenAIEntries(entries []openAIModelEntry, defaultModel string) ([]Model, bool, error) {
	models := make([]Model, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		id := strings.TrimSpace(entry.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		label := strings.TrimSpace(entry.Name)
		if label == "" {
			label = strings.TrimSpace(entry.DisplayName)
		}
		if label == "" {
			label = id
		}
		models = append(models, Model{
			ID:      id,
			Label:   label,
			Default: defaultModel != "" && id == defaultModel,
		})
	}
	if len(models) == 0 {
		return nil, true, nil
	}
	return models, false, nil
}

func errModelsListUnavailable(reason string) error {
	return fmt.Errorf("暂时无法获取模型列表：%s", reason)
}
