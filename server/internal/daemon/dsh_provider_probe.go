package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Talking to a provider gateway.
//
// A preset is a route: an endpoint, the protocol it speaks and the models it
// serves. Two of those three can be asked for instead of typed, and this file
// is where they are asked for:
//
//	GET  {baseURL}/models        the catalog, and the cheapest proof that the
//	                             key authenticates at all
//	POST {baseURL}/chat/completions
//	     {baseURL}/messages      a minimal round trip, the only step that
//	                             exposes "the key is valid but this plan
//	                             cannot use this model"
//
// The second step is not optional. A gateway answers /models for a key bound
// to a cancelled billing cycle (403 MODEL_NOT_IN_PLAN for a model outside the
// plan, 429 RATE_LIMITED for an exhausted period), and its own dashboard shows
// the quota as healthy while every request fails. DENE-680 is the record of
// what "authenticates fine" costs when it is mistaken for "works".
//
// Everything that fails here fails with a KIND. The provider's prose is an
// English fallback for logs and non-UI clients; the kind plus its parameters is
// what a localized surface renders, because "your quota is exhausted" and
// "your key is bound to a cancelled period, regenerate it" are the same HTTP
// status and lead the user down opposite paths.

// Probe failure kinds. Stable wire values: the UI switches on them and needs a
// default branch for one it does not know.
const (
	// providerProbeKindInvalidCredential — the key was rejected outright.
	providerProbeKindInvalidCredential = "invalid_credential"
	// providerProbeKindMissingCredential — there is no key to try.
	providerProbeKindMissingCredential = "missing_credential"
	// providerProbeKindModelNotInPlan — authenticated, but the plan excludes
	// the model.
	providerProbeKindModelNotInPlan = "model_not_in_plan"
	// providerProbeKindRateLimited — authenticated, but the quota or billing
	// period refuses the request.
	providerProbeKindRateLimited = "rate_limited"
	// providerProbeKindUnknownModel — the endpoint does not serve this id.
	providerProbeKindUnknownModel = "unknown_model"
	// providerProbeKindEndpointMismatch — the request went to a protocol the
	// model does not answer on.
	providerProbeKindEndpointMismatch = "endpoint_mismatch"
	// providerProbeKindMixedProtocols — the preset lists models that need
	// different wire protocols, and a route speaks exactly one.
	providerProbeKindMixedProtocols = "mixed_protocols"
	// providerProbeKindUnreachable — no HTTP response at all.
	providerProbeKindUnreachable = "unreachable"
	// providerProbeKindModelsUnavailable — the endpoint has no /models. A
	// gateway like this is why manual entry survives as a fallback.
	providerProbeKindModelsUnavailable = "models_unavailable"
	// providerProbeKindEmptyModelList — /models answered with nothing.
	providerProbeKindEmptyModelList = "empty_model_list"
	// providerProbeKindProviderError — anything else the gateway answered.
	providerProbeKindProviderError = "provider_error"
)

// The two wire protocols a DSH route can speak, and the path each one appends
// to the route's baseURL. These are the values written to `api` in
// settings.yaml and the values `supported_endpoints` reports.
const (
	providerAPIOpenAICompletions = "openai-completions"
	providerAPIAnthropicMessages = "anthropic-messages"

	providerEndpointChatCompletions = "/chat/completions"
	providerEndpointMessages        = "/messages"

	// providerThinkingFormatDeepSeek is the compat switch a DeepSeek-family
	// gateway needs. Omitting it does not fail: the reasoning tokens are
	// counted as content, the parser sees no content, and the reply renders
	// blank with no error anywhere.
	providerThinkingFormatDeepSeek = "deepseek"

	providerAnthropicVersion = "2023-06-01"
)

const (
	// providerProbeTimeout bounds one probe request. Two of them run per
	// health check, so the pair stays well inside the server's 60-second
	// running timeout for a provider-config request.
	providerProbeTimeout = 15 * time.Second

	// providerProbePrompt is fixed and trivial on purpose: this is a liveness
	// check, and the answer is discarded.
	providerProbePrompt = "ping"

	// providerProbeMaxTokens is the smallest completion a gateway accepts.
	providerProbeMaxTokens = 1

	// providerProbeDetailLimit caps how much of a gateway's error body is
	// echoed back. The body is not ours to trust and the useful part is
	// always at the front.
	providerProbeDetailLimit = 400
)

// providerProbeClient performs every outbound probe. A package-level value so
// a test can install a fake gateway without threading a client through the
// provider-driver interface.
var providerProbeClient = &http.Client{Timeout: providerProbeTimeout}

// providerConfigFailure is a provider-config action failure that carries a
// machine-readable kind and parameters next to its human-readable message.
type providerConfigFailure struct {
	Kind    string
	Message string
	Params  map[string]string
}

func (f *providerConfigFailure) Error() string { return f.Message }

func providerFailure(kind, message string, params map[string]string) *providerConfigFailure {
	return &providerConfigFailure{Kind: kind, Message: message, Params: params}
}

// providerDiscoveredModel is one entry of the gateway's own model list. ID is
// carried verbatim: the provider's id is the truth, and escaping it for
// Multica's provider/model string is a later, display-side concern.
type providerDiscoveredModel struct {
	ID                 string
	Name               string
	ContextWindow      int64
	MaxTokens          int64
	SupportedEndpoints []string
}

// dshProviderRoute is what a successful health check learned about the route.
type dshProviderRoute struct {
	API            string
	ThinkingFormat string
	Model          providerDiscoveredModel
}

// dshVerifyRequest is one save-time health check.
type dshVerifyRequest struct {
	BaseURL string
	APIKey  string
	// ModelID is the model the completion runs against.
	ModelID string
	// ModelIDs is every model the preset will carry after this save. A route
	// speaks one wire protocol, so a preset whose models need two cannot be
	// written and is refused here rather than at the next agent run.
	ModelIDs []string
	// API is the protocol the caller declared. It is only consulted when the
	// gateway's own list does not describe the model's endpoints: the
	// provider's answer wins, a caller's choice stands when there is no
	// answer, and neither is guessed at.
	API string
}

// dshVerifyProviderRoute runs both probe steps and returns what the write
// needs: the protocol to record and the compat switch the model family needs.
func dshVerifyProviderRoute(ctx context.Context, request dshVerifyRequest) (*dshProviderRoute, error) {
	models, err := fetchProviderModels(ctx, request.BaseURL, request.APIKey)
	if err != nil {
		return nil, err
	}

	// Every model the preset will carry has to exist, and all of them have to
	// agree on a protocol: one route field carries one protocol, so a preset
	// that mixed them would send half its models down the wrong endpoint.
	answered := map[string]string{}
	var answeredOrder []string
	for _, id := range request.ModelIDs {
		model, ok := findProviderModel(models, id)
		if !ok {
			return nil, unknownProviderModel(id)
		}
		if len(model.SupportedEndpoints) == 0 {
			continue
		}
		protocol := providerProtocolForModel(model, "")
		if _, seen := answered[protocol]; !seen {
			answered[protocol] = id
			answeredOrder = append(answeredOrder, protocol)
		}
	}
	if len(answeredOrder) > 1 {
		first, second := answeredOrder[0], answeredOrder[1]
		return nil, providerFailure(providerProbeKindMixedProtocols,
			fmt.Sprintf("Models %q and %q need different protocols (%s and %s), and one preset carries one protocol. Split them into two presets.",
				answered[first], answered[second], first, second),
			map[string]string{
				"model":          answered[first],
				"other_model":    answered[second],
				providerParamAPI: first,
				"other_api":      second,
			})
	}

	model, ok := findProviderModel(models, request.ModelID)
	if !ok {
		return nil, unknownProviderModel(request.ModelID)
	}
	api := providerProtocolForModel(model, request.API)
	if len(answeredOrder) == 1 {
		api = answeredOrder[0]
	}
	if err := probeProviderChat(ctx, request.BaseURL, request.APIKey, api, model.ID); err != nil {
		return nil, err
	}
	return &dshProviderRoute{
		API:            api,
		ThinkingFormat: providerThinkingFormatForModel(model.ID),
		Model:          model,
	}, nil
}

// providerParamAPI is the key a surface reads the recorded protocol from.
const providerParamAPI = "api"

func unknownProviderModel(id string) *providerConfigFailure {
	return providerFailure(providerProbeKindUnknownModel,
		fmt.Sprintf("Model %q does not exist on this endpoint. Pick one from the fetched model list.", id),
		map[string]string{"model": id})
}

// findProviderModel matches the model the save names. An empty id means "the
// preset's first model", which is the one activate would pick.
func findProviderModel(models []providerDiscoveredModel, id string) (providerDiscoveredModel, bool) {
	if len(models) == 0 {
		return providerDiscoveredModel{}, false
	}
	if strings.TrimSpace(id) == "" {
		return models[0], true
	}
	for _, model := range models {
		if model.ID == id {
			return model, true
		}
	}
	return providerDiscoveredModel{}, false
}

// providerProtocolForModel decides which wire protocol the model is requested
// with. `supported_endpoints` is the gateway answering the question itself;
// model-family guessing is only the fallback for a gateway that stays silent.
func providerProtocolForModel(model providerDiscoveredModel, declared string) string {
	for _, endpoint := range model.SupportedEndpoints {
		if strings.TrimSpace(endpoint) == providerEndpointMessages {
			return providerAPIAnthropicMessages
		}
	}
	if len(model.SupportedEndpoints) > 0 {
		return providerAPIOpenAICompletions
	}
	if strings.TrimSpace(declared) != "" {
		return strings.TrimSpace(declared)
	}
	return providerAPIOpenAICompletions
}

// providerThinkingFormatForModel returns the compat switch a model family
// needs, or "" when it needs none.
func providerThinkingFormatForModel(modelID string) string {
	if strings.Contains(strings.ToLower(modelID), "deepseek") {
		return providerThinkingFormatDeepSeek
	}
	return ""
}

// fetchProviderModels is probe step one: the catalog, with the key attached.
// It authenticates without spending tokens, which is why it runs first.
func fetchProviderModels(ctx context.Context, baseURL, apiKey string) ([]providerDiscoveredModel, error) {
	endpoint, err := providerEndpointURL(baseURL, "/models")
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, providerFailure(providerProbeKindUnreachable,
			fmt.Sprintf("Could not build a model-list request for %s: %v", baseURL, err), nil)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Accept", "application/json")

	status, body, err := doProviderRequest(request)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, classifyModelListFailure(status, body, baseURL)
	}

	models, err := parseProviderModels(body)
	if err != nil {
		return nil, providerFailure(providerProbeKindProviderError,
			fmt.Sprintf("%s answered the model list with something that is not JSON: %v", baseURL, err),
			map[string]string{"status": fmt.Sprintf("%d", status)})
	}
	if len(models) == 0 {
		return nil, providerFailure(providerProbeKindEmptyModelList,
			fmt.Sprintf("%s returned an empty model list.", baseURL), nil)
	}
	return models, nil
}

// probeProviderChat is probe step two: one minimal completion. This is the
// only step that separates "the key authenticates" from "the key can actually
// run this model", and a health check that stops at step one reports the
// cancelled-billing-cycle key as healthy.
func probeProviderChat(ctx context.Context, baseURL, apiKey, api, modelID string) error {
	path := providerEndpointChatCompletions
	if api == providerAPIAnthropicMessages {
		path = providerEndpointMessages
	}
	endpoint, err := providerEndpointURL(baseURL, path)
	if err != nil {
		return err
	}

	body, err := json.Marshal(map[string]any{
		"model":      modelID,
		"max_tokens": providerProbeMaxTokens,
		"messages":   []map[string]string{{"role": "user", "content": providerProbePrompt}},
	})
	if err != nil {
		return providerFailure(providerProbeKindProviderError,
			fmt.Sprintf("Could not build the probe request: %v", err), nil)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return providerFailure(providerProbeKindUnreachable,
			fmt.Sprintf("Could not build a request for %s: %v", baseURL, err), nil)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if api == providerAPIAnthropicMessages {
		request.Header.Set("x-api-key", apiKey)
		request.Header.Set("anthropic-version", providerAnthropicVersion)
	} else {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}

	status, responseBody, err := doProviderRequest(request)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return classifyGatewayFailure(status, responseBody, modelID)
	}
	return nil
}

// doProviderRequest performs one probe request. The message it returns never
// contains the key: only the endpoint, the transport error and the response
// body reach it, and the response body is the gateway's own words.
func doProviderRequest(request *http.Request) (int, []byte, error) {
	response, err := providerProbeClient.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return 0, nil, providerFailure(providerProbeKindUnreachable,
				"The provider did not answer in time.", nil)
		}
		return 0, nil, providerFailure(providerProbeKindUnreachable,
			fmt.Sprintf("Could not reach %s: %v", request.URL.Host, err), nil)
	}
	defer response.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(response.Body, providerProbeDetailLimit))
	if readErr != nil {
		return 0, nil, providerFailure(providerProbeKindUnreachable,
			fmt.Sprintf("Could not read the response from %s: %v", request.URL.Host, readErr), nil)
	}
	return response.StatusCode, body, nil
}

// providerEndpointURL joins a route's baseURL with the path a probe appends. A
// baseURL that is not http(s) is refused here rather than handed to the
// transport, because this value decides where the user's key is sent.
func providerEndpointURL(baseURL, path string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
		return "", providerFailure(providerProbeKindUnreachable,
			fmt.Sprintf("%q is not an http(s) endpoint.", baseURL), nil)
	}
	return trimmed + path, nil
}

// providerModelsResponse is the OpenAI-compatible listing. Two spellings of
// the capacity field are in the wild and both mean the same thing.
type providerModelsResponse struct {
	Data []struct {
		ID                 string   `json:"id"`
		Name               string   `json:"name"`
		ContextLength      int64    `json:"context_length"`
		ContextWindow      int64    `json:"context_window"`
		MaxTokens          int64    `json:"max_tokens"`
		SupportedEndpoints []string `json:"supported_endpoints"`
	} `json:"data"`
}

func parseProviderModels(body []byte) ([]providerDiscoveredModel, error) {
	var parsed providerModelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	models := make([]providerDiscoveredModel, 0, len(parsed.Data))
	for _, entry := range parsed.Data {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			continue
		}
		window := entry.ContextLength
		if window == 0 {
			window = entry.ContextWindow
		}
		models = append(models, providerDiscoveredModel{
			ID:                 id,
			Name:               strings.TrimSpace(entry.Name),
			ContextWindow:      window,
			MaxTokens:          entry.MaxTokens,
			SupportedEndpoints: entry.SupportedEndpoints,
		})
	}
	return models, nil
}

// ---------------------------------------------------------------------------
// Error translation
// ---------------------------------------------------------------------------

// The gateway's own words, matched on the parts that survive a wording change.
var (
	providerPlanRejectedPattern   = regexp.MustCompile(`MODEL_NOT_IN_PLAN`)
	providerRateLimitedPattern    = regexp.MustCompile(`RATE_LIMITED|rate limit`)
	providerUnsupportedModelMatch = regexp.MustCompile(`(?i)is not supported on this endpoint`)
	providerWrongEndpointPattern  = regexp.MustCompile(`(?i)\bUse\s+(/[A-Za-z0-9._/-]+)\s+for\b`)
	providerResetAtPattern        = regexp.MustCompile(`(?i)resets?\s+at\s+([0-9]{4}-[0-9]{2}-[0-9]{2}[T ][0-9:.]+(?:Z|[+-][0-9:]+)?)`)
)

// classifyModelListFailure translates a failed /models call. Against a
// cancelled billing period this is already a 429, which is why the rate-limit
// branch is here too and not only on the chat step.
func classifyModelListFailure(status int, body []byte, baseURL string) error {
	detail := strings.TrimSpace(string(body))

	switch {
	case status == http.StatusNotFound || status == http.StatusMethodNotAllowed:
		return providerFailure(providerProbeKindModelsUnavailable,
			fmt.Sprintf("%s does not publish a model list (HTTP %d). Enter the model id by hand.", baseURL, status),
			map[string]string{"status": fmt.Sprintf("%d", status)})
	case status == http.StatusUnauthorized:
		return providerFailure(providerProbeKindInvalidCredential,
			fmt.Sprintf("The provider rejected this API key (HTTP %d). Check the key and try again.", status),
			map[string]string{"status": fmt.Sprintf("%d", status)})
	case status == http.StatusForbidden && !providerPlanRejectedPattern.MatchString(detail):
		return providerFailure(providerProbeKindInvalidCredential,
			fmt.Sprintf("The provider refused this API key (HTTP %d). Check the key and try again.", status),
			map[string]string{"status": fmt.Sprintf("%d", status)})
	}
	return classifyGatewayFailure(status, body, "")
}

// classifyGatewayFailure maps one gateway rejection onto a kind. modelID is
// empty for the listing step, which never names a model.
func classifyGatewayFailure(status int, body []byte, modelID string) error {
	detail := strings.TrimSpace(string(body))
	params := map[string]string{"status": fmt.Sprintf("%d", status)}
	if modelID != "" {
		params["model"] = modelID
	}
	if detail != "" {
		params["detail"] = detail
	}

	switch {
	case status == http.StatusUnauthorized:
		return providerFailure(providerProbeKindInvalidCredential,
			fmt.Sprintf("The provider rejected this API key (HTTP %d). Check the key and try again.", status), params)

	case providerPlanRejectedPattern.MatchString(detail):
		return providerFailure(providerProbeKindModelNotInPlan,
			fmt.Sprintf("The current plan does not include model %q. Pick another model or upgrade the plan.", modelID),
			params)

	case status == http.StatusTooManyRequests || providerRateLimitedPattern.MatchString(detail):
		reset := providerResetPhrase(detail)
		if reset != "" {
			params["reset_at_local"] = reset
		}
		message := "The provider quota is used up"
		if reset != "" {
			message += fmt.Sprintf("; it resets at %s", reset)
		}
		// The sentence that matters: the provider's own dashboard shows this
		// key as healthy, so a user told only "quota exhausted" waits for a
		// reset that will never come. DENE-680 records that dead end.
		message += ". If the provider dashboard shows quota available, this key is bound to a cancelled billing cycle — regenerate the key in the provider dashboard."
		params["action"] = "regenerate_key"
		return providerFailure(providerProbeKindRateLimited, message, params)

	case providerUnsupportedModelMatch.MatchString(detail):
		return providerFailure(providerProbeKindUnknownModel,
			fmt.Sprintf("Model %q does not exist on this endpoint. Pick one from the fetched model list.", modelID),
			params)

	case providerWrongEndpointPattern.MatchString(detail):
		wanted := providerWrongEndpointPattern.FindStringSubmatch(detail)[1]
		return providerFailure(providerProbeKindEndpointMismatch,
			fmt.Sprintf("Model %q must be called on %s. The route is chosen from the model's supported endpoints.", modelID, wanted),
			params)

	case status == http.StatusForbidden:
		return providerFailure(providerProbeKindInvalidCredential,
			fmt.Sprintf("The provider refused this API key (HTTP %d). Check the key and try again.", status), params)
	}

	return providerFailure(providerProbeKindProviderError,
		fmt.Sprintf("The provider rejected the request (HTTP %d): %s", status, providerProbeDetail(detail)), params)
}

// providerResetPhrase renders the reset instant in the machine's own timezone,
// because that is the clock the user is looking at when they decide whether to
// wait. An unparseable instant is dropped rather than shown wrong.
func providerResetPhrase(detail string) string {
	match := providerResetAtPattern.FindStringSubmatch(detail)
	if match == nil {
		return ""
	}
	raw := match[1]
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.Local().Format("2006-01-02 15:04")
		}
	}
	return ""
}

func providerProbeDetail(detail string) string {
	if detail == "" {
		return "no response body"
	}
	return detail
}
