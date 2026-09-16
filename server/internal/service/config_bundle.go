package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
)

const (
	ConfigBundleFormat        = "multica.workspace-config"
	ConfigBundleSchemaVersion = 1
	ConfigBundleMaxBytes      = 20 << 20
	configImportRenameMax     = 50

	ConflictFail      = "fail"
	ConflictOverwrite = "overwrite"
	ConflictRename    = "rename"
	ConflictSkip      = "skip"

	secretReasonMaterial  = "secret_material"
	secretReasonWriteOnly = "write_only"
	secretReasonDenylist  = "key_name_denylist"

	ActionCreated = "created"
	ActionUpdated = "updated"
	ActionRenamed = "renamed"
	ActionSkipped = "skipped"
	ActionFailed  = "failed"

	BatchCommitted    = "committed"
	BatchRolledBack   = "rolled_back"
	BatchNotAttempted = "not_attempted"
	BatchPreview      = "preview"
)

var configEntityTypes = []string{
	"workspace",
	"labels",
	"issue_statuses",
	"issue_properties",
	"skills",
	"mcp_servers",
	"agents",
	"system_agents",
	"squads",
	"projects",
	"autopilots",
	"quick_actions",
	"issue_views",
}

var secretKeyDenylist = map[string]struct{}{
	"token": {}, "secret": {}, "password": {}, "passwd": {},
	"apikey": {}, "authorization": {}, "privatekey": {},
	"credential": {}, "credentials": {},
}

// secretKeyName reports whether a JSON key names secret material. Keys are
// split into words on separators and camelCase boundaries, so compound names
// such as access_token, GITHUB_TOKEN, clientSecret and x-api-key match while
// max_tokens does not. Adjacent word pairs cover api_key / private_key.
func secretKeyName(key string) bool {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	runes := []rune(key)
	for i, r := range runes {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
		case unicode.IsUpper(r) && i > 0 && unicode.IsLower(runes[i-1]):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	for i, w := range words {
		if _, hit := secretKeyDenylist[w]; hit {
			return true
		}
		if i+1 < len(words) {
			if _, hit := secretKeyDenylist[w+words[i+1]]; hit {
				return true
			}
		}
	}
	return false
}

// ConfigBundle is the on-wire workspace configuration package.
type ConfigBundle struct {
	Format             string              `json:"format"`
	SchemaVersion      int                 `json:"schema_version"`
	BundleID           string              `json:"bundle_id"`
	ExportedAt         time.Time           `json:"exported_at"`
	Source             ConfigBundleSource  `json:"source"`
	Options            ConfigBundleOptions `json:"options"`
	Entities           ConfigEntities      `json:"entities"`
	Integrations       []ConfigIntegration `json:"integrations"`
	PluginsToReinstall []ConfigPlugin      `json:"plugins_to_reinstall"`
	SecretsOmitted     []SecretOmitted     `json:"secrets_omitted"`
	Stats              map[string]int      `json:"stats"`
}

type ConfigBundleSource struct {
	WorkspaceID   string `json:"workspace_id"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	IssuePrefix   string `json:"issue_prefix"`
	ServerVersion string `json:"server_version,omitempty"`
	ExportedBy    string `json:"exported_by"`
}

type ConfigBundleOptions struct {
	IncludeArchived bool `json:"include_archived"`
}

type ConfigEntities struct {
	Workspace       *ConfigWorkspace    `json:"workspace,omitempty"`
	Labels          []ConfigLabel       `json:"labels,omitempty"`
	IssueStatuses   []ConfigIssueStatus `json:"issue_statuses,omitempty"`
	IssueProperties []ConfigProperty    `json:"issue_properties,omitempty"`
	Skills          []ConfigSkill       `json:"skills,omitempty"`
	McpServers      []ConfigMcpServer   `json:"mcp_servers,omitempty"`
	Agents          []ConfigAgent       `json:"agents,omitempty"`
	SystemAgents    []ConfigSystemAgent `json:"system_agents,omitempty"`
	Squads          []ConfigSquad       `json:"squads,omitempty"`
	Projects        []ConfigProject     `json:"projects,omitempty"`
	Autopilots      []ConfigAutopilot   `json:"autopilots,omitempty"`
	QuickActions    []ConfigQuickAction `json:"quick_actions,omitempty"`
	IssueViews      []ConfigIssueView   `json:"issue_views,omitempty"`
}

type ConfigWorkspace struct {
	Settings              json.RawMessage `json:"settings"`
	Context               string          `json:"context"`
	Repos                 json.RawMessage `json:"repos"`
	IssuePrefix           string          `json:"issue_prefix"`
	AttributionFailClosed bool            `json:"attribution_fail_closed"`
}

type ConfigLabel struct {
	SourceID     string `json:"source_id"`
	ResourceType string `json:"resource_type"`
	Name         string `json:"name"`
	Color        string `json:"color"`
	Description  string `json:"description"`
	Archived     bool   `json:"archived,omitempty"`
}

type ConfigIssueStatus struct {
	SourceID    string  `json:"source_id"`
	Key         string  `json:"key"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Category    string  `json:"category"`
	Color       string  `json:"color"`
	Position    float64 `json:"position"`
	IsSystem    bool    `json:"is_system"`
	Archived    bool    `json:"archived,omitempty"`
}

type ConfigProperty struct {
	SourceID    string          `json:"source_id"`
	Name        string          `json:"name"`
	Type        string          `json:"type"`
	Description string          `json:"description"`
	Icon        string          `json:"icon"`
	Config      json.RawMessage `json:"config"`
	Position    float64         `json:"position"`
	Archived    bool            `json:"archived,omitempty"`
}

type ConfigSkill struct {
	SourceID    string            `json:"source_id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Content     string            `json:"content"`
	Config      json.RawMessage   `json:"config"`
	LabelIDs    []string          `json:"label_ids"`
	Files       []ConfigSkillFile `json:"files"`
	Archived    bool              `json:"archived,omitempty"`
}

type ConfigSkillFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type ConfigMcpServer struct {
	SourceID  string `json:"source_id"`
	Name      string `json:"name"`
	Transport string `json:"transport"`
}

type ConfigAgent struct {
	SourceID string `json:"source_id"`
	// SourceRuntimeID is the agent's runtime on the SOURCE instance. It is
	// meaningless as an id on the target, and exists only so a cross-instance
	// import can look the source runtime up in runtimes_hint and match its
	// provider / mode / profile against the target's own runtimes (DENE-364).
	SourceRuntimeID          string                   `json:"source_runtime_id,omitempty"`
	Name                     string                   `json:"name"`
	Description              string                   `json:"description"`
	Instructions             string                   `json:"instructions"`
	AvatarURL                *string                  `json:"avatar_url"`
	RuntimeMode              string                   `json:"runtime_mode"`
	RuntimeConfig            json.RawMessage          `json:"runtime_config"`
	CustomArgs               json.RawMessage          `json:"custom_args"`
	CustomEnv                json.RawMessage          `json:"custom_env"`
	McpConfig                json.RawMessage          `json:"mcp_config"`
	Model                    *string                  `json:"model"`
	ThinkingLevel            *string                  `json:"thinking_level"`
	ServiceTier              *string                  `json:"service_tier"`
	Visibility               string                   `json:"visibility"`
	PermissionMode           string                   `json:"permission_mode"`
	MaxConcurrentTasks       int32                    `json:"max_concurrent_tasks"`
	ConversationStarters     json.RawMessage          `json:"conversation_starters"`
	DisabledRuntimeSkills    json.RawMessage          `json:"disabled_runtime_skills"`
	ComposioToolkitAllowlist []string                 `json:"composio_toolkit_allowlist"`
	Skills                   []ConfigAgentSkill       `json:"skills"`
	LabelIDs                 []string                 `json:"label_ids"`
	McpServers               []ConfigAgentMcp         `json:"mcp_servers"`
	InvocationTargets        []ConfigInvocationTarget `json:"invocation_targets"`
	Archived                 bool                     `json:"archived,omitempty"`
}

type ConfigAgentSkill struct {
	Skill   string `json:"skill"`
	Enabled bool   `json:"enabled"`
}

type ConfigAgentMcp struct {
	Server  string `json:"server"`
	Enabled bool   `json:"enabled"`
}

type ConfigInvocationTarget struct {
	TargetType string  `json:"target_type"`
	TargetID   *string `json:"target_id"`
}

type ConfigSystemAgent struct {
	SystemKey             string          `json:"system_key"`
	Instructions          string          `json:"instructions"`
	Model                 *string         `json:"model"`
	ThinkingLevel         *string         `json:"thinking_level"`
	ServiceTier           *string         `json:"service_tier"`
	ConversationStarters  json.RawMessage `json:"conversation_starters"`
	DisabledRuntimeSkills json.RawMessage `json:"disabled_runtime_skills"`
}

type ConfigSquad struct {
	SourceID     string              `json:"source_id"`
	Name         string              `json:"name"`
	Description  string              `json:"description"`
	Instructions string              `json:"instructions"`
	AvatarURL    *string             `json:"avatar_url"`
	LeaderID     string              `json:"leader_id"`
	Members      []ConfigSquadMember `json:"members"`
	Archived     bool                `json:"archived,omitempty"`
}

type ConfigSquadMember struct {
	MemberType string `json:"member_type"`
	MemberID   string `json:"member_id"`
	Role       string `json:"role"`
}

type ConfigProject struct {
	SourceID    string                  `json:"source_id"`
	Title       string                  `json:"title"`
	Description string                  `json:"description"`
	Icon        *string                 `json:"icon"`
	Status      string                  `json:"status"`
	Priority    string                  `json:"priority"`
	Lead        *ConfigPolymorphicRef   `json:"lead"`
	StartDate   *string                 `json:"start_date"`
	DueDate     *string                 `json:"due_date"`
	Resources   []ConfigProjectResource `json:"resources"`
}

type ConfigProjectResource struct {
	ResourceType string          `json:"resource_type"`
	ResourceRef  json.RawMessage `json:"resource_ref"`
	Label        *string         `json:"label"`
	Position     int32           `json:"position"`
}

type ConfigAutopilot struct {
	SourceID           string                   `json:"source_id"`
	Title              string                   `json:"title"`
	Description        string                   `json:"description"`
	Assignee           *ConfigPolymorphicRef    `json:"assignee"`
	ProjectID          *string                  `json:"project_id"`
	ExecutionMode      string                   `json:"execution_mode"`
	IssueTitleTemplate *string                  `json:"issue_title_template"`
	Status             string                   `json:"status"`
	Triggers           []ConfigAutopilotTrigger `json:"triggers"`
	Subscribers        []ConfigAutopilotPerson  `json:"subscribers"`
	Collaborators      []ConfigAutopilotPerson  `json:"collaborators"`
}

type ConfigAutopilotTrigger struct {
	Kind           string          `json:"kind"`
	Enabled        bool            `json:"enabled"`
	CronExpression *string         `json:"cron_expression"`
	Timezone       *string         `json:"timezone"`
	Label          *string         `json:"label"`
	Provider       string          `json:"provider"`
	EventFilters   json.RawMessage `json:"event_filters"`
	WebhookToken   json.RawMessage `json:"webhook_token"`
	SigningSecret  json.RawMessage `json:"signing_secret"`
}

type ConfigAutopilotPerson struct {
	UserType string `json:"user_type"`
	UserID   string `json:"user_id"`
}

type ConfigQuickAction struct {
	SourceID    string                `json:"source_id"`
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Assignee    *ConfigPolymorphicRef `json:"assignee"`
	Prompt      string                `json:"prompt"`
	Visibility  string                `json:"visibility"`
	Status      string                `json:"status"`
}

type ConfigIssueView struct {
	SourceID          string          `json:"source_id"`
	Name              string          `json:"name"`
	ScopeType         string          `json:"scope_type"`
	ScopeID           *string         `json:"scope_id"`
	ScopeVariant      *string         `json:"scope_variant"`
	Visibility        string          `json:"visibility"`
	DefinitionVersion int32           `json:"definition_version"`
	Query             json.RawMessage `json:"query"`
	Display           json.RawMessage `json:"display"`
}

type ConfigPolymorphicRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type ConfigIntegration struct {
	Kind         string `json:"kind"`
	AccountLogin string `json:"account_login,omitempty"`
	AccountType  string `json:"account_type,omitempty"`
	Provider     string `json:"provider,omitempty"`
	InstanceURL  string `json:"instance_url,omitempty"`
	ChannelType  string `json:"channel_type,omitempty"`
	Agent        string `json:"agent,omitempty"`
	Name         string `json:"name,omitempty"`
	Transport    string `json:"transport,omitempty"`
}

type ConfigPlugin struct {
	PluginKey     string          `json:"plugin_key"`
	Version       string          `json:"version"`
	Enabled       bool            `json:"enabled"`
	GrantedScopes json.RawMessage `json:"granted_scopes"`
	Config        json.RawMessage `json:"config"`
}

type SecretOmitted struct {
	Entity   string         `json:"entity"`
	SourceID string         `json:"source_id,omitempty"`
	Name     string         `json:"name,omitempty"`
	Field    string         `json:"field"`
	Reason   string         `json:"reason"`
	Hint     map[string]any `json:"hint,omitempty"`
}

type ConfigImportReport struct {
	Applied       bool                `json:"applied"`
	BundleID      string              `json:"bundle_id"`
	OnConflict    string              `json:"on_conflict"`
	Batches       []ConfigImportBatch `json:"batches"`
	UnmappedRefs  []UnmappedRef       `json:"unmapped_refs"`
	SecretsToFill []SecretToFill      `json:"secrets_to_fill"`
	Warnings      []ConfigWarning     `json:"warnings"`
	Stats         ConfigImportStats   `json:"stats"`
}

type ConfigImportBatch struct {
	EntityType  string             `json:"entity_type"`
	BatchStatus string             `json:"batch_status"`
	Items       []ConfigImportItem `json:"items"`
}

type ConfigImportItem struct {
	SourceID string `json:"source_id,omitempty"`
	Name     string `json:"name"`
	Action   string `json:"action"`
	TargetID string `json:"target_id,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type UnmappedRef struct {
	Entity     string `json:"entity"`
	SourceID   string `json:"source_id"`
	Field      string `json:"field"`
	RefType    string `json:"ref_type"`
	RefID      string `json:"ref_id"`
	Resolution string `json:"resolution"`
}

type SecretToFill struct {
	Entity     string `json:"entity"`
	TargetID   string `json:"target_id,omitempty"`
	Name       string `json:"name,omitempty"`
	Field      string `json:"field"`
	Path       string `json:"path,omitempty"`
	WebhookURL string `json:"webhook_url,omitempty"`
}

type ConfigWarning struct {
	Code  string `json:"code"`
	Count int    `json:"count,omitempty"`
}

type ConfigImportStats struct {
	Created int `json:"created"`
	Updated int `json:"updated"`
	Renamed int `json:"renamed"`
	Skipped int `json:"skipped"`
	Failed  int `json:"failed"`
}

type ConfigImportOptions struct {
	IncludeArchived        bool  `json:"include_archived"`
	ActivateAutopilots     bool  `json:"activate_autopilots"`
	ApplyWorkspaceSettings *bool `json:"apply_workspace_settings"`
	ApplyIssuePrefix       bool  `json:"apply_issue_prefix"`
	// AutoBindRuntimes binds an imported agent to the single matching runtime
	// on the target when there is exactly one candidate (DENE-364). Absent
	// means on: a bundle written before this switch existed still lands usable,
	// and the unique-candidate rule is what keeps the write unambiguous.
	AutoBindRuntimes *bool `json:"auto_bind_runtimes"`
}

// AutoBindRuntimesEnabled reports the effective auto-bind switch. Unlike the
// DENE-363 switches, an absent value means ON, because binding nothing is the
// broken state this option exists to fix.
func (o ConfigImportOptions) AutoBindRuntimesEnabled() bool {
	return o.AutoBindRuntimes == nil || *o.AutoBindRuntimes
}

type ConfigImportRequest struct {
	Bundle     ConfigBundle        `json:"bundle"`
	DryRun     *bool               `json:"dry_run"`
	OnConflict string              `json:"on_conflict"`
	Include    []string            `json:"include"`
	Options    ConfigImportOptions `json:"options"`
}

// ImportError is an HTTP-mappable import failure.
type ImportError struct {
	Status int
	Code   string
	Msg    string
	Report *ConfigImportReport
}

func (e *ImportError) Error() string { return e.Msg }

func ValidOnConflict(v string) bool {
	switch v {
	case "", ConflictFail, ConflictOverwrite, ConflictRename, ConflictSkip:
		return true
	}
	return false
}

func ValidEntityType(v string) bool {
	for _, t := range configEntityTypes {
		if t == v {
			return true
		}
	}
	return false
}

func includedSet(include []string, present map[string]bool) map[string]bool {
	out := map[string]bool{}
	if len(include) == 0 {
		for _, t := range configEntityTypes {
			if present[t] || t == "workspace" {
				out[t] = true
			}
		}
		return out
	}
	for _, t := range include {
		out[t] = true
	}
	return out
}

func textPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	s := t.String
	return &s
}

func rawOrEmpty(b []byte, empty string) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage(empty)
	}
	return json.RawMessage(b)
}

func rawOrNull(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("null")
	}
	return json.RawMessage(b)
}

func jsonNull() json.RawMessage { return json.RawMessage("null") }

func parseUUID(s string) (pgtype.UUID, error) {
	return util.ParseUUID(s)
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}

func stripGatewayToken(runtimeConfig []byte) (json.RawMessage, bool) {
	if len(runtimeConfig) == 0 {
		return json.RawMessage("{}"), false
	}
	var obj map[string]any
	if err := json.Unmarshal(runtimeConfig, &obj); err != nil {
		return json.RawMessage(runtimeConfig), false
	}
	had := false
	if gw, ok := obj["gateway"].(map[string]any); ok {
		if _, exists := gw["token"]; exists {
			had = true
			gw["token"] = nil
			obj["gateway"] = gw
		}
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return json.RawMessage(runtimeConfig), had
	}
	return out, had
}

// keepTargetGatewayToken copies the target agent's existing gateway.token into
// an imported runtime_config whose token was stripped, so overwrite never
// clears a secret the bundle cannot carry.
func keepTargetGatewayToken(imported json.RawMessage, target []byte) json.RawMessage {
	var tgt map[string]any
	if err := json.Unmarshal(target, &tgt); err != nil {
		return imported
	}
	tgtGW, _ := tgt["gateway"].(map[string]any)
	token, _ := tgtGW["token"].(string)
	if token == "" {
		return imported
	}
	var obj map[string]any
	if err := json.Unmarshal(imported, &obj); err != nil {
		return imported
	}
	gw, ok := obj["gateway"].(map[string]any)
	if !ok {
		// The bundle carries no gateway block at all; replacing the target's
		// would erase its token, so keep the target's gateway intact.
		obj["gateway"] = tgtGW
	} else if gw["token"] != nil {
		return imported
	} else {
		gw["token"] = token
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return imported
	}
	return out
}

// secretArgPlaceholder replaces a secret value inside agent custom_args. It is
// never written to a target agent: import restores or drops it.
const secretArgPlaceholder = "__multica_secret_omitted__"

// argFlagName returns the bare name of a CLI flag ("--api-key" -> "api-key")
// and whether the argument looks like a flag at all.
func argFlagName(arg string) (string, bool) {
	if !strings.HasPrefix(arg, "-") {
		return "", false
	}
	name := strings.TrimLeft(arg, "-")
	return name, name != ""
}

// redactSecretArgs masks secret values in a custom_args JSON array. It covers
// "--api-key=VALUE", "GITHUB_TOKEN=VALUE" and "--api-key VALUE" forms, where
// the key is judged by secretKeyName. It returns the masked array and how many
// values were masked.
func redactSecretArgs(raw []byte) (json.RawMessage, int) {
	var args []any
	if len(raw) == 0 || json.Unmarshal(raw, &args) != nil {
		return json.RawMessage(raw), 0
	}
	masked := 0
	for i := 0; i < len(args); i++ {
		s, ok := args[i].(string)
		if !ok {
			continue
		}
		if eq := strings.Index(s, "="); eq > 0 {
			if secretKeyName(strings.TrimLeft(s[:eq], "-")) && s[eq+1:] != "" {
				args[i] = s[:eq+1] + secretArgPlaceholder
				masked++
			}
			continue
		}
		name, isFlag := argFlagName(s)
		if !isFlag || !secretKeyName(name) || i+1 >= len(args) {
			continue
		}
		next, ok := args[i+1].(string)
		if !ok || strings.HasPrefix(next, "-") {
			continue
		}
		args[i+1] = secretArgPlaceholder
		masked++
		i++
	}
	if masked == 0 {
		return json.RawMessage(raw), 0
	}
	out, err := json.Marshal(args)
	if err != nil {
		return json.RawMessage("[]"), masked
	}
	return out, masked
}

// restoreSecretArgs resolves placeholders in imported custom_args. A masked
// value is taken from the target agent's args for the same key when present
// (overwrite must not clear an existing secret); otherwise the key/value pair
// is dropped so no placeholder reaches the agent command line.
func restoreSecretArgs(imported []byte, target []byte) []byte {
	if !strings.Contains(string(imported), secretArgPlaceholder) {
		return imported
	}
	var args []any
	if json.Unmarshal(imported, &args) != nil {
		return []byte("[]")
	}
	var tgt []any
	_ = json.Unmarshal(target, &tgt)
	targetValue := func(key string) (string, bool) {
		for j, v := range tgt {
			s, _ := v.(string)
			if eq := strings.Index(s, "="); eq > 0 && s[:eq] == key {
				return s[eq+1:], true
			}
			if s == key && j+1 < len(tgt) {
				if next, ok := tgt[j+1].(string); ok && !strings.HasPrefix(next, "-") {
					return next, true
				}
			}
		}
		return "", false
	}
	out := make([]any, 0, len(args))
	for i := 0; i < len(args); i++ {
		s, ok := args[i].(string)
		if !ok {
			out = append(out, args[i])
			continue
		}
		if eq := strings.Index(s, "="); eq > 0 && s[eq+1:] == secretArgPlaceholder {
			if v, ok := targetValue(s[:eq]); ok && v != secretArgPlaceholder {
				out = append(out, s[:eq+1]+v)
			}
			continue
		}
		if i+1 < len(args) {
			if next, ok := args[i+1].(string); ok && next == secretArgPlaceholder {
				if v, ok := targetValue(s); ok && v != secretArgPlaceholder {
					out = append(out, s, v)
				}
				i++
				continue
			}
		}
		if s == secretArgPlaceholder {
			continue
		}
		out = append(out, s)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return []byte("[]")
	}
	return b
}

func optionalText(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *s, Valid: true}
}

func mcpTransportFromFlags(configType string, hasCommand, hasURL bool) string {
	declared := strings.ToLower(strings.TrimSpace(configType))
	if declared != "" {
		switch declared {
		case "local", "stdio":
			return "stdio"
		case "remote", "http", "streamable-http":
			return "http"
		}
		return declared
	}
	if hasCommand {
		return "stdio"
	}
	if hasURL {
		return "http"
	}
	return "unknown"
}

func newBundleID() string {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return id.String()
}

func sanitizeBundleSecrets(bundle *ConfigBundle) {
	if bundle == nil {
		return
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		return
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return
	}
	var extra []SecretOmitted
	sanitizeValue(tree, "", &extra)
	out, err := json.Marshal(tree)
	if err != nil {
		return
	}
	if err := json.Unmarshal(out, bundle); err != nil {
		return
	}
	bundle.SecretsOmitted = append(bundle.SecretsOmitted, extra...)
}

func sanitizeValue(v any, path string, extra *[]SecretOmitted) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if secretKeyName(k) {
				if child != nil {
					t[k] = nil
					*extra = append(*extra, SecretOmitted{
						Field:  joinJSONPath(path, k),
						Reason: secretReasonDenylist,
					})
				}
				continue
			}
			sanitizeValue(child, joinJSONPath(path, k), extra)
		}
	case []any:
		for i, child := range t {
			sanitizeValue(child, fmt.Sprintf("%s[%d]", path, i), extra)
		}
	}
}

func joinJSONPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

func bundleContainsSecret(bundle *ConfigBundle) error {
	if bundle == nil {
		return nil
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		return nil
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return nil
	}
	return findForbiddenSecret(tree, "")
}

func findForbiddenSecret(v any, path string) error {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			lk := strings.ToLower(k)
			full := joinJSONPath(path, k)
			switch lk {
			case "custom_env", "mcp_config", "signing_secret", "webhook_token":
				if child != nil {
					return fmt.Errorf("%s", full)
				}
			case "token":
				if strings.HasSuffix(strings.ToLower(path), "gateway") && child != nil {
					return fmt.Errorf("%s", full)
				}
			}
			if err := findForbiddenSecret(child, full); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range t {
			if err := findForbiddenSecret(child, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func emptyJSONArray() json.RawMessage  { return json.RawMessage("[]") }
func emptyJSONObject() json.RawMessage { return json.RawMessage("{}") }
