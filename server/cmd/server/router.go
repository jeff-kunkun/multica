package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/daemonws"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/officemcp"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/storage"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/llm"
)

var defaultOrigins = []string{
	"http://localhost:3000", // Next.js dev
	"http://localhost:5173", // electron-vite dev
	"http://localhost:5174", // electron-vite dev (fallback port)
}

// corsAllowedHeaders must list every header the browser clients send. A header
// missing here fails the preflight, so the request never reaches the handler at
// all — the failure looks nothing like "the server ignored my header".
// X-Client-Capabilities in particular was daemon-only (a Go client, never
// preflighted) until the web app started advertising chat-draft-restore-v1 on
// cancel.
var corsAllowedHeaders = []string{
	"Accept",
	"Authorization",
	"Content-Type",
	"Idempotency-Key",
	"If-Match",
	"X-Workspace-ID",
	"X-Workspace-Slug",
	"X-Request-ID",
	"X-Agent-ID",
	"X-Task-ID",
	"X-CSRF-Token",
	"X-Client-Platform",
	"X-Client-Version",
	"X-Client-OS",
	"X-Client-Capabilities",
}

// corsExposedHeaders lists response headers browser clients are allowed to read.
// Without this a custom response header is silently unreadable from JS on a
// cross-origin request (only the CORS-safelisted response headers are exposed by
// default) — the header arrives on the wire and then disappears, which looks
// exactly like the server never sent it.
//
// Referencing the handler constant rather than re-typing the string keeps a
// rename from quietly switching the signal off (MUL-5492).
var corsExposedHeaders = []string{
	"ETag",
	"X-Request-ID",
	handler.HeaderCommentsTruncated,
	handler.HeaderTimelineTruncated,
	handler.HeaderActiveRunsTruncated,
	handler.HeaderAgentTasksNextCursor,
}

func allowedOrigins() []string {
	raw := strings.TrimSpace(os.Getenv("CORS_ALLOWED_ORIGINS"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("FRONTEND_ORIGIN"))
	}
	if raw == "" {
		return defaultOrigins
	}

	parts := strings.Split(raw, ",")
	origins := make([]string, 0, len(parts))
	for _, part := range parts {
		origin := strings.TrimSpace(part)
		if origin != "" {
			origins = append(origins, origin)
		}
	}
	if len(origins) == 0 {
		return defaultOrigins
	}
	return origins
}

// appURLFromEnv resolves the user-facing web app URL. It prefers
// MULTICA_APP_URL and falls back to FRONTEND_ORIGIN, matching how the backend
// resolves the app URL elsewhere (handler.daemonSetupURLsFromEnv) and the CLI
// login flow (cmd/multica tryResolveAppURL). Empty when neither is set.
func appURLFromEnv() string {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_APP_URL")), "/"); v != "" {
		return v
	}
	return strings.TrimRight(strings.TrimSpace(os.Getenv("FRONTEND_ORIGIN")), "/")
}

// parseTrustedProxies parses a comma-separated list of CIDR prefixes from the
// MULTICA_TRUSTED_PROXIES env var. Invalid entries are dropped with a single
// warn-line per entry rather than crashing the server — a typo in one CIDR
// shouldn't take the whole API down. Returns nil for empty input, which the
// rate limiter treats as "trust no proxy headers, use RemoteAddr only".
func parseTrustedProxies(raw string) []netip.Prefix {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []netip.Prefix
	for _, part := range strings.Split(raw, ",") {
		s := strings.TrimSpace(part)
		if s == "" {
			continue
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			slog.Warn("MULTICA_TRUSTED_PROXIES: ignoring invalid CIDR",
				"value", s, "error", err)
			continue
		}
		out = append(out, p)
	}
	return out
}

// normalizeServerVersion maps the unstamped "dev" default (main.go's
// `version` var, unchanged when the binary wasn't built with
// -X main.version=<tag>) to an empty string. handler.Config.ServerVersion
// feeds /api/config's server_version field with omitempty, so an empty
// string hides the Help popover's version row instead of rendering
// "Server version dev" for a local `go build`/`go run` or a self-hosted
// `docker build` without --build-arg VERSION.
func normalizeServerVersion(v string) string {
	if v == "dev" {
		return ""
	}
	return v
}

// NewRouter creates the fully-configured Chi router with all middleware and routes.
// rdb is optional: when non-nil the runtime pending-request stores are
// swapped for Redis-backed implementations so multiple API nodes share the
// same pending queue (required for multi-node prod). This should be a request
// path Redis client, not the realtime relay's blocking read client. A nil rdb
// keeps the default in-memory stores which are fine for single-node dev and
// tests.
func NewRouter(pool *pgxpool.Pool, hub *realtime.Hub, bus *events.Bus, analyticsClient analytics.Client, rdb redis.UniversalClient) chi.Router {
	r, _ := NewRouterWithOptions(pool, hub, bus, analyticsClient, rdb, RouterOptions{})
	return r
}

type RouterOptions struct {
	HTTPMetrics     *obsmetrics.HTTPMetrics
	BusinessMetrics *obsmetrics.BusinessMetrics
	DaemonHub       *daemonws.Hub
	DaemonWakeup    service.TaskWakeupNotifier
	FeatureFlags    *featureflag.Service
	// HeartbeatScheduler, when non-nil, replaces the default synchronous
	// passthrough scheduler on the constructed Handler. main.go injects a
	// BatchedHeartbeatScheduler here so the caller can also drive Run/Stop;
	// tests leave this nil and get the legacy synchronous behavior.
	HeartbeatScheduler handler.HeartbeatScheduler
	// LLMMaxRetries carries the parsed MULTICA_LLM_MAX_RETRIES budget. Unlike
	// its three MULTICA_LLM_* siblings it is injected rather than read here,
	// because an invalid value must fail the boot and only main() can exit —
	// terminating the process from inside a router constructor would also kill
	// any test that happened to have the variable set. nil means unset, which
	// is what tests and NewRouter get.
	LLMMaxRetries *llm.RetryOverride
	// LLMDisableThinking carries the parsed MULTICA_LLM_DISABLE_THINKING
	// switch. It follows its LLMMaxRetries sibling in being injected rather
	// than read here, for the same fail-the-boot-in-main-only reason: the raw
	// value is validated by parseLLMDisableThinking before the router exists.
	LLMDisableThinking bool
}

// NewRouterWithOptions builds the fully-configured Chi router and
// returns the *handler.Handler it was constructed from. Callers that
// need to drive background lifecycle on services attached to the
// handler (e.g. starting background workers under a long-running
// context, calling Wait on shutdown) use the returned handler;
// callers that only need the HTTP handler (tests, the simple
// NewRouter shim) discard the second value.
func NewRouterWithOptions(pool *pgxpool.Pool, hub *realtime.Hub, bus *events.Bus, analyticsClient analytics.Client, rdb redis.UniversalClient, opts RouterOptions) (chi.Router, *handler.Handler) {
	queries := db.New(pool)
	emailSvc := service.NewEmailService()
	daemonHub := opts.DaemonHub
	if daemonHub == nil {
		daemonHub = daemonws.NewHub()
	}

	// Initialize storage with S3 as primary, fallback to local
	var store storage.Storage
	s3 := storage.NewS3StorageFromEnv()
	if s3 != nil {
		store = s3
	} else {
		local := storage.NewLocalStorageFromEnv()
		if local != nil {
			store = local
		}
	}

	cfSigner := auth.NewCloudFrontSignerFromEnv()
	origins := allowedOrigins()

	signupConfig := handler.Config{
		AllowSignup:              os.Getenv("ALLOW_SIGNUP") != "false",
		AllowedEmails:            splitAndTrim(os.Getenv("ALLOWED_EMAILS")),
		AllowedEmailDomains:      splitAndTrim(os.Getenv("ALLOWED_EMAIL_DOMAINS")),
		DisableWorkspaceCreation: os.Getenv("DISABLE_WORKSPACE_CREATION") == "true",
		VCSIntegrationEnabled:    os.Getenv("MULTICA_VCS_INTEGRATION_ENABLED") == "true",
		PublicURL:                strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_PUBLIC_URL")), "/"),
		AppURL:                   appURLFromEnv(),
		TrustedProxies:           parseTrustedProxies(os.Getenv("MULTICA_TRUSTED_PROXIES")),
		CloudURL:                 strings.TrimSpace(os.Getenv("MULTICA_CLOUD_URL")),
		AttachmentDownloadMode:   os.Getenv("ATTACHMENT_DOWNLOAD_MODE"),
		AttachmentDownloadURLTTL: envDuration("ATTACHMENT_DOWNLOAD_URL_TTL", 30*time.Minute),
		AttachmentFrameAncestors: origins,
		LLMAPIKey:                strings.TrimSpace(os.Getenv("MULTICA_LLM_API_KEY")),
		LLMBaseURL:               strings.TrimSpace(os.Getenv("MULTICA_LLM_BASE_URL")),
		LLMDefaultModel:          strings.TrimSpace(os.Getenv("MULTICA_LLM_DEFAULT_MODEL")),
		LLMMaxRetries:            opts.LLMMaxRetries,
		LLMDisableThinking:       opts.LLMDisableThinking,
		ServerVersion:            normalizeServerVersion(version),
	}
	h := handler.New(queries, pool, hub, bus, emailSvc, store, cfSigner, analyticsClient, signupConfig, daemonHub)
	invitationRateLimits := handler.DefaultInvitationRateLimits()
	invitationRateLimits.Actor.Limit = envNonNegativeInt("RATE_LIMIT_INVITATION_ACTOR_10M", invitationRateLimits.Actor.Limit)
	invitationRateLimits.Workspace.Limit = envNonNegativeInt("RATE_LIMIT_INVITATION_WORKSPACE_24H", invitationRateLimits.Workspace.Limit)
	invitationRateLimits.Recipient.Limit = envNonNegativeInt("RATE_LIMIT_INVITATION_RECIPIENT_24H", invitationRateLimits.Recipient.Limit)
	h.InvitationRateLimiters = handler.NewMemoryInvitationRateLimiters(invitationRateLimits)
	h.Metrics = opts.BusinessMetrics
	h.FeatureFlags = opts.FeatureFlags
	h.TaskService.FeatureFlags = opts.FeatureFlags
	h.TaskService.Metrics = opts.BusinessMetrics
	h.IssueService.Metrics = opts.BusinessMetrics
	if opts.DaemonWakeup != nil {
		h.TaskService.Wakeup = opts.DaemonWakeup
		if notifier, ok := opts.DaemonWakeup.(handler.RuntimeProfileRefreshNotifier); ok {
			h.DaemonProfileRefresh = notifier
		}
		if notifier, ok := opts.DaemonWakeup.(handler.WorkspaceSetRefreshNotifier); ok {
			h.DaemonWorkspaceRefresh = notifier
		}
		if notifier, ok := opts.DaemonWakeup.(handler.DaemonTaskSupplementNotifier); ok {
			h.DaemonTaskSupplement = notifier
		}
		if notifier, ok := opts.DaemonWakeup.(handler.RuntimeGoneNotifier); ok {
			h.DaemonRuntimeGone = notifier
		}
	}
	if rdb != nil {
		h.UpdateStore = handler.NewRedisUpdateStore(rdb)
		h.LivenessStore = handler.NewRedisLivenessStore(rdb)
		h.InvitationRateLimiters = handler.NewRedisInvitationRateLimiters(rdb, invitationRateLimits)
	}

	// VCS at-rest encryption: the box encrypts per-workspace access tokens and
	// webhook secrets for token-based providers (Forgejo / Gitea / GitLab).
	// Without it, connect/webhook handlers return 503 (so a misconfigured
	// self-host never stores plaintext secrets).
	if vcsKey, err := secretbox.LoadKey("MULTICA_VCS_SECRET_KEY"); err == nil {
		box, err := secretbox.New(vcsKey)
		if err != nil {
			slog.Error("vcs: secretbox.New failed; vcs integration disabled", "error", err)
		} else {
			h.VCSSecretBox = box
			slog.Info("vcs integration enabled")
		}
	} else {
		slog.Info("vcs integration disabled (MULTICA_VCS_SECRET_KEY not set)")
	}

	if opts.HeartbeatScheduler != nil {
		h.HeartbeatScheduler = opts.HeartbeatScheduler
	}
	// Auth caches: PAT cache is shared between the regular Auth middleware,
	// the DaemonAuth fallback (mul_) path, and the revoke handler
	// (invalidate). DaemonTokenCache backs the DaemonAuth mdt_ path. Both
	// constructors return nil when rdb is nil — every consumer handles that
	// as "no cache, always hit DB".
	patCache := auth.NewPATCache(rdb)
	daemonTokenCache := auth.NewDaemonTokenCache(rdb)
	h.PATCache = patCache
	h.DaemonTokenCache = daemonTokenCache
	h.MembershipCache = auth.NewMembershipCache(rdb)

	// Cloud PAT verifier: validates mcn_ tokens against Multica Cloud
	// Fleet. Returns nil when no Cloud URL is configured — the Auth /
	// DaemonAuth middlewares treat nil as "mcn_ not supported" and
	// reject with 401, instead of falling through to mul_/JWT paths.
	// Configured by MULTICA_CLOUD_URL.
	cloudPATVerifier := auth.NewCloudPATVerifier(auth.CloudPATVerifierConfig{
		FleetBaseURL: signupConfig.CloudURL,
		Redis:        rdb,
	})

	// Empty-claim cache: lets the daemon poll path skip a Postgres
	// scan when a recent check confirmed the runtime had no queued
	// task. Returns nil when rdb is nil — TaskService treats that
	// as "no cache, always hit DB" (existing behavior).
	h.TaskService.EmptyClaim = service.NewEmptyClaimCache(rdb)
	// Stale-dispatch reclaim has a separate schedule because an empty queued
	// verdict cannot represent a claim response lost after commit. Missing or
	// failed Redis state keeps the historical PostgreSQL fallback.
	h.TaskService.ReclaimCheck = service.NewReclaimCheckCache(rdb)

	// Wire WS heartbeat after stores are finalized so the WS path uses the
	// same (possibly Redis-backed) stores as the HTTP path.
	daemonHub.SetHeartbeatHandler(h.HandleDaemonWSHeartbeat)
	// WS-first claim (MUL-4257): route daemon:rpc_request frames (e.g.
	// tasks.claim) through the same handlers as the HTTP endpoints.
	daemonHub.SetRPCHandler(h.DaemonRPCHandler)
	health := newServerHealth(pool)

	r := chi.NewRouter()
	// root is the top-level router; the MCP endpoint replays tool calls
	// through it from inside a route group where `r` is shadowed.
	root := r

	// Global middleware
	r.Use(chimw.RequestID)
	r.Use(middleware.ClientMetadata)
	r.Use(middleware.RequestLogger)
	if opts.HTTPMetrics != nil {
		r.Use(opts.HTTPMetrics.Middleware)
	}
	r.Use(chimw.Recoverer)
	r.Use(middleware.ContentSecurityPolicy)

	// Share allowed origins with WebSocket origin checker.
	realtime.SetAllowedOrigins(origins)

	// Share the same trusted-proxy CIDRs (MULTICA_TRUSTED_PROXIES) so the
	// WebSocket origin check honors X-Forwarded-Host only from trusted proxies,
	// using one config source instead of a parallel one.
	realtime.SetTrustedProxies(signupConfig.TrustedProxies)

	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   origins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   corsAllowedHeaders,
		ExposedHeaders:   corsExposedHeaders,
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// Health / readiness checks
	r.Get("/health", health.liveHandler)
	r.Get("/readyz", health.readyHandler)
	r.Get("/healthz", health.readyHandler)

	// Realtime subsystem metrics — connection counts, slow-client evictions,
	// and per-event-type send QPS counters. Exposed as JSON so it can be
	// scraped by ops or surfaced in the admin UI without adding a Prometheus
	// dependency. See MUL-1138 (Phase 0).
	//
	// Access is restricted (MUL-1342): when REALTIME_METRICS_TOKEN is set,
	// callers must present it via Authorization: Bearer <token>. When the
	// env var is unset the handler only serves loopback callers so local
	// dev keeps working without exposing the metrics on a public listener.
	r.Get("/health/realtime", realtimeMetricsHandler(os.Getenv("REALTIME_METRICS_TOKEN")))

	// WebSocket
	mc := &membershipChecker{queries: queries}
	pr := &patResolver{queries: queries, cache: patCache}
	slugResolver := realtime.SlugResolver(func(ctx context.Context, slug string) (string, error) {
		ws, err := queries.GetWorkspaceBySlug(ctx, slug)
		if err != nil {
			return "", err
		}
		return util.UUIDToString(ws.ID), nil
	})
	r.Get("/ws", func(w http.ResponseWriter, r *http.Request) {
		realtime.HandleWebSocket(hub, mc, pr, slugResolver, w, r)
	})

	// Local file serving (when using local storage). Served through the
	// handler so /uploads/* carries the same preview security headers as the
	// /api/attachments download endpoint; self-hosted split-origin/same-origin
	// clients can then iframe-preview PDFs/HTML fetched straight from the
	// static route instead of hitting the global frame-ancestors 'none' CSP.
	// See MUL-3821 / #4477.
	if _, ok := store.(*storage.LocalStorage); ok {
		r.Get("/uploads/*", h.ServeLocalUpload)
	}

	// Capability-authenticated attachment download (MUL-5292). Public by
	// necessity: a native download (Electron's webContents.downloadURL, a
	// cross-site webview <img>) carries neither Authorization nor a session
	// cookie, so there is nothing here for middleware.Auth to read. The
	// short-lived, single-attachment signature in the query is the credential,
	// and it is only ever minted by the AUTHENTICATED GET
	// /api/attachments/{id} after that request's membership check passed.
	// The authenticated /api/attachments/{id}/download route below is
	// unchanged — this one is purely additive.
	r.Get("/api/attachments/{id}/signed-download", h.DownloadAttachmentWithCapability)

	// Avatar serving. Public for the same reason as the capability download
	// above: the auth cookie is SameSite=Strict, so an auth-gated URL cannot
	// be a native <img src> from Desktop / mobile webview or a split-origin
	// self-hosted web app. The HMAC signature in the path is the credential.
	// It covers the storage key, only image keys resolve, and the object must
	// be avatar-class — see server/internal/handler/avatar.go (MUL-5393 /
	// #6024).
	r.Get("/api/avatars/{sig}/*", h.ServeAvatar)

	// Auth (public) — per-IP rate limiting.
	if rdb == nil {
		slog.Warn("auth rate limiting disabled: REDIS_URL not configured")
	}
	trustedProxies := middleware.ParseTrustedProxies(os.Getenv("RATE_LIMIT_TRUSTED_PROXIES"))
	authRL := middleware.RateLimit(rdb, envPositiveInt("RATE_LIMIT_AUTH", 5), time.Minute, trustedProxies)
	authVerifyRL := middleware.RateLimit(rdb, envPositiveInt("RATE_LIMIT_AUTH_VERIFY", 20), time.Minute, trustedProxies)
	r.With(authRL).Post("/auth/send-code", h.SendCode)
	r.With(authVerifyRL).Post("/auth/verify-code", h.VerifyCode)
	r.With(authRL).Post("/auth/google", h.GoogleLogin)
	r.With(authRL).Post("/auth/login", h.PasswordLogin)
	r.With(authRL).Post("/auth/signup", h.PasswordSignup)
	r.With(authRL).Post("/auth/reset-password", h.PasswordReset)
	r.Post("/auth/logout", h.Logout)

	// Public API
	r.Get("/api/config", h.GetConfig)
	// Public share-link preview — no auth: shows the workspace name/slug and
	// inviter so a not-yet-logged-in visitor can see what they're joining.
	r.Get("/api/share-links/{code}", h.GetShareLinkInfo)

	// GitHub App webhook (no Multica auth — requests are authenticated via
	// HMAC-SHA256 signature in the handler) and post-install setup callback.
	r.Post("/api/webhooks/github", h.HandleGitHubWebhook)
	r.Get("/api/github/setup", h.GitHubSetupCallback)
	// VCS webhook for token-based providers (Forgejo / Gitea / GitLab). No Multica
	// auth — authenticated per-connection by the provider's signature scheme;
	// the connection id in the path selects the workspace, provider, and
	// decryption secret.
	r.Post("/api/webhooks/vcs/{connectionId}", h.HandleVCSWebhook)

	// Daemon API routes (require daemon token or valid user token)
	r.Route("/api/daemon", func(r chi.Router) {
		r.Use(middleware.DaemonAuth(queries, patCache, daemonTokenCache, cloudPATVerifier))

		r.Post("/register", h.DaemonRegister)
		r.Post("/deregister", h.DaemonDeregister)
		r.Post("/heartbeat", h.DaemonHeartbeat)
		r.Get("/ws", h.DaemonWebSocket)
		r.Get("/workspaces", h.ListDaemonWorkspaces)
		r.Get("/workspaces/{workspaceId}/repos", h.GetDaemonWorkspaceRepos)
		r.Get("/workspaces/{workspaceId}/runtime-profiles", h.DaemonListRuntimeProfiles)

		r.Post("/runtimes/{runtimeId}/tasks/claim", h.ClaimTaskByRuntime)
		// Canonical machine-level batch claim (MUL-4257). `/claim` is a
		// transitional alias; the daemon coordinator targets the canonical
		// path.
		r.Post("/tasks/claim", h.ClaimTasksByRuntime)
		r.Post("/claim", h.ClaimTasksByRuntime)
		r.Post("/runtimes/{runtimeId}/tasks/{taskId}/prepare-lease", h.ExtendTaskPrepareLease)
		r.Get("/runtimes/{runtimeId}/tasks/pending", h.ListPendingTasksByRuntime)
		r.Post("/runtimes/{runtimeId}/update/{updateId}/result", h.ReportUpdateResult)

		r.Get("/tasks/{taskId}/status", h.GetTaskStatus)
		r.Post("/tasks/{taskId}/start", h.StartTask)
		r.Post("/tasks/{taskId}/supplements/claim", h.ClaimTaskSupplement)
		r.Post("/tasks/{taskId}/supplements/{commentId}/ack", h.AckTaskSupplement)
		r.Post("/tasks/{taskId}/wait-local-directory", h.MarkTaskWaitingLocalDirectory)
		r.Post("/tasks/{taskId}/progress", h.ReportTaskProgress)
		r.Post("/tasks/{taskId}/complete", h.CompleteTask)
		r.Post("/tasks/{taskId}/fail", h.FailTask)
		r.Post("/tasks/{taskId}/usage", h.ReportTaskUsage)
		r.Post("/tasks/{taskId}/messages", h.ReportTaskMessages)
		r.Get("/tasks/{taskId}/messages", h.ListTaskMessages)
		r.Post("/tasks/{taskId}/cancel-ack", h.AckTaskCancelled)

		r.Post("/workspaces/{workspaceId}/issues/gc-check", h.BatchIssueGCCheck)
		r.Get("/issues/{issueId}/gc-check", h.GetIssueGCCheck)
		r.Get("/chat-sessions/{sessionId}/gc-check", h.GetChatSessionGCCheck)
		r.Get("/tasks/{taskId}/gc-check", h.GetTaskGCCheck)

		r.Post("/runtimes/{runtimeId}/recover-orphans", h.RecoverOrphanedTasks)
		r.Post("/tasks/{taskId}/session", h.PinTaskSession)
	})

	r.Group(func(r chi.Router) {
		r.Use(middleware.Auth(queries, patCache, cloudPATVerifier, cfSigner))
		r.Use(middleware.RefreshCloudFrontCookies(cfSigner))

		// --- User-scoped routes (no workspace context required) ---
		r.Get("/api/me", h.GetMe)
		r.Patch("/api/me", h.UpdateMe)
		r.Post("/api/cli-token", h.IssueCliToken)
		// Sliding session renewal for clients that hold the session as a
		// string (Desktop, mobile). Browsers get theirs re-issued inline by
		// middleware.Auth and never call this (MUL-7436).
		r.Post("/api/auth/refresh", h.RefreshSession)
		r.Post("/api/upload-file", h.UploadFile)
		r.Post("/api/feedback", h.CreateFeedback)
		r.With(handler.RequireHumanActor).Post("/api/client-usage", h.UpsertClientUsage)

		// Note (MUL-4309): the generic OpenAI-compatible passthrough endpoints
		// (POST /api/llm/v1/chat/completions[/stream]) were intentionally
		// removed. Exposing a general LLM proxy backed by the deployment's own
		// key let any logged-in user run arbitrary completions on our dime.
		// LLM access is now server-internal only (see pkg/llm); anything the
		// web/client needs must go through a purpose-built business endpoint
		// that fixes the prompt/model server-side (e.g. chat title generation).

		// Attachment download — user-scoped (auth-only), NOT
		// workspace-scoped. The handler self-resolves the workspace
		// from the attachment row and enforces membership inside, so
		// this route is callable as a native browser <img>/<video>
		// src that cannot attach X-Workspace-Slug / X-Workspace-ID
		// headers. Persisting `/api/attachments/<id>/download` into
		// comment markdown depends on this — see MUL-3130. The
		// metadata / delete endpoints below stay workspace-scoped
		// because they are JSON-API consumers that always have
		// workspace context.
		r.Get("/api/attachments/{id}/download", h.DownloadAttachment)
		// Markdown links may point to an attachment in a different workspace.
		// Resolve that workspace from the row and check membership before minting
		// a fresh URL; the workspace-scoped metadata route stays unchanged.
		r.Get("/api/attachments/{id}/download-metadata", h.GetAttachmentForDownload)

		r.Route("/api/workspaces", func(r chi.Router) {
			r.Get("/", h.ListWorkspaces)
			r.Post("/", h.CreateWorkspace)
			r.Route("/{id}", func(r chi.Router) {
				// Member-level access
				r.Group(func(r chi.Router) {
					r.Use(middleware.RequireWorkspaceMemberFromURL(queries, "id"))
					r.Get("/", h.GetWorkspace)
					r.Get("/members", h.ListMembersWithUser)
					r.Post("/leave", h.LeaveWorkspace)
					r.Get("/invitations", h.ListWorkspaceInvitations)
					// Listing GitHub installations is member-visible so the
					// integrations tab no longer renders blank for non-admins;
					// the handler strips the management handle and adds a
					// can_manage hint so the UI can gate connect/disconnect.
					r.Get("/github/installations", h.ListGitHubInstallations)
					// VCS connections (Forgejo / Gitea / GitLab) — member-visible
					// for the same reason as GitHub installations; connect /
					// disconnect are admin-gated in the group below.
					r.Get("/vcs/connections", h.ListVCSConnections)
					// Custom runtime profiles — listing/reading is member-visible
					// (the Runtime page renders for everyone; create/edit/delete
					// are admin-gated below).
					r.Get("/runtime-profiles", h.ListRuntimeProfiles)
					r.Get("/runtime-profiles/{profileId}", h.GetRuntimeProfile)
					// The workspace MCP library — member-visible so an agent
					// owner can see what is available to add to their agent.
					// The payload is names and transports only; the stored
					// entries are write-only.
					r.Get("/mcp-servers", h.ListWorkspaceMcpServers)
				})
				// Owner-only access
				r.Group(func(r chi.Router) {
					r.Use(middleware.RequireWorkspaceRoleFromURL(queries, "id", "owner"))
					r.Put("/", h.UpdateWorkspace)
					r.Patch("/", h.UpdateWorkspace)
					r.Post("/members", h.CreateInvitation)
					r.Delete("/members/{memberId}", h.DeleteMember)
					r.Delete("/invitations/{invitationId}", h.RevokeInvitation)
					// Curating the shared MCP library is an admin action.
					// Creating an entry binds it to no agent; an agent owner
					// adds it to their own agent through the agent routes.
					r.Post("/mcp-servers", h.CreateWorkspaceMcpServer)
					r.Put("/mcp-servers/{serverId}", h.UpdateWorkspaceMcpServer)
					r.Delete("/mcp-servers/{serverId}", h.DeleteWorkspaceMcpServer)
					r.Post("/share-links", h.CreateShareLink)
					r.Delete("/share-links/{linkId}", h.RevokeShareLink)
					r.Get("/share-links", h.ListShareLinks)
					// Custom runtime profile mutations (admin-only).
					r.Post("/runtime-profiles", h.CreateRuntimeProfile)
					r.Patch("/runtime-profiles/{profileId}", h.UpdateRuntimeProfile)
					r.Put("/runtime-profiles/{profileId}", h.UpdateRuntimeProfile)
					r.Delete("/runtime-profiles/{profileId}", h.DeleteRuntimeProfile)
				})
				// Owner-only access
				r.With(middleware.RequireWorkspaceRoleFromURL(queries, "id", "owner")).Delete("/", h.DeleteWorkspace)

				// GitHub integration — connect / disconnect remain admin-only;
				// the read-only list endpoint lives in the member-level group
				// above so non-admins can see the workspace's connection state.
				r.Group(func(r chi.Router) {
					r.Use(middleware.RequireWorkspaceRoleFromURL(queries, "id", "owner"))
					r.Get("/github/connect", h.GitHubConnect)
					r.Get("/github/installations/{installationId}/repositories", h.ListGitHubInstallationRepositories)
					r.Delete("/github/installations/{installationId}", h.DeleteGitHubInstallation)
					// VCS connect / disconnect / webhook regeneration (admin-only).
					r.Post("/vcs/connections", h.ConnectVCS)
					r.Post("/vcs/connections/{connectionId}/rotate-webhook", h.RotateVCSConnectionWebhook)
					r.Delete("/vcs/connections/{connectionId}", h.DeleteVCSConnection)
				})

			})
		})

		// User-scoped invitation routes (no workspace context required)
		r.Get("/api/invitations", h.ListMyInvitations)
		r.Get("/api/invitations/{id}", h.GetMyInvitation)
		r.Post("/api/invitations/{id}/accept", h.AcceptInvitation)
		r.Post("/api/invitations/{id}/decline", h.DeclineInvitation)
		r.Post("/api/share-links/join", h.JoinByShareLink)

		// Project-manager Bot surface (DENE-2016). Both resolve the caller's
		// Bot from its token and act in the Bot's own workspace, so the Bot
		// sends no workspace header. The MCP tools call this router
		// in-process with the same token.
		r.Get("/api/bot/events", h.ListBotEvents)
		r.Put("/api/bot/webhook", h.SetBotWebhook)
		r.Handle("/api/mcp", officemcp.New(root, func(ctx context.Context, userID string) (officemcp.Bot, bool) {
			bot, ok := h.BotForUser(ctx, userID)
			if !ok {
				return officemcp.Bot{}, false
			}
			return officemcp.Bot{
				ID:          util.UUIDToString(bot.ID),
				Name:        bot.Name,
				WorkspaceID: util.UUIDToString(bot.WorkspaceID),
				CanRelease:  bot.CanRelease,
			}, true
		}, normalizeServerVersion(version)))

		r.Route("/api/tokens", func(r chi.Router) {
			r.Get("/", h.ListPersonalAccessTokens)
			r.Post("/", h.CreatePersonalAccessToken)
			r.Post("/current/renew", h.RenewCurrentPersonalAccessToken)
			r.Delete("/{id}", h.RevokePersonalAccessToken)
		})

		// --- Workspace-scoped routes (all require workspace membership) ---
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireWorkspaceMember(queries))

			// Assignee frequency
			r.Get("/api/assignee-frequency", h.GetAssigneeFrequency)

			// Local search index sync for Web/Desktop (MUL-7754). Human clients
			// only: agents search through /api/issues/search.
			r.Route("/api/search-index", func(r chi.Router) {
				r.Use(handler.RequireHumanActor)
				r.Use(h.RequireLocalSearchIndex)
				r.Get("/manifest", h.GetSearchIndexManifest)
				r.Get("/snapshot", h.GetSearchIndexSnapshot)
				r.Post("/changes", h.ListSearchIndexChanges)
			})

			// Issues
			r.Route("/api/issues", func(r chi.Router) {
				r.Post("/table/groups", h.ListIssueTableGroups)
				r.Post("/table/rows", h.ListIssueTableRows)
				r.Post("/table/facets", h.ListIssueTableFacets)
				r.Get("/search", h.SearchIssues)
				r.Get("/child-progress", h.ChildIssueProgress)
				r.Get("/children", h.ListChildrenByParents)
				r.Get("/grouped", h.ListGroupedIssues)
				r.Get("/", h.ListIssues)
				// POST twin of GET /api/issues for oversized filter sets
				// (agents-working ids facet) — see QueryIssues.
				r.Post("/query", h.QueryIssues)
				r.Post("/", h.CreateIssue)
				r.Post("/preview-trigger", h.PreviewIssueTrigger)
				r.Post("/batch-update", h.BatchUpdateIssues)
				r.Post("/batch-delete", h.BatchDeleteIssues)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", h.GetIssue)
					r.Put("/", h.UpdateIssue)
					r.Post("/move", h.MoveIssue)
					r.Delete("/", h.DeleteIssue)
					r.Post("/comments/trigger-preview", h.PreviewCommentTriggers)
					r.Post("/comments", h.CreateComment)
					r.Get("/comments", h.ListComments)
					r.Get("/timeline", h.ListTimeline)
					r.Get("/plans", h.ListPlans)
					r.Post("/plans", h.ProposePlan)
					r.Post("/plans/{version}/approve", h.ApprovePlan)
					r.Get("/subscribers", h.ListIssueSubscribers)
					r.Post("/subscribe", h.SubscribeToIssue)
					r.Post("/unsubscribe", h.UnsubscribeFromIssue)
					r.Post("/unsubscribe/subtree", h.UnsubscribeFromIssueSubtree)
					r.Get("/active-task", h.GetActiveTaskForIssue)
					r.Post("/tasks/{taskId}/cancel", h.CancelTask)
					r.With(handler.RequireHumanActor).Post("/tasks/{taskId}/supplements", h.CreateTaskSupplement)
					r.With(handler.RequireHumanActor).Post("/tasks/{taskId}/supplements/{commentId}/retry", h.RetryTaskSupplement)
					r.Post("/rerun", h.RerunIssue)
					r.Get("/task-runs", h.ListTasksByIssue)
					r.Get("/usage", h.GetIssueUsage)
					r.Post("/reactions", h.AddIssueReaction)
					r.Delete("/reactions", h.RemoveIssueReaction)
					r.Get("/attachments", h.ListAttachments)
					r.Get("/children", h.ListChildIssues)
					r.Get("/labels", h.ListLabelsForIssue)
					r.Post("/labels", h.AttachLabel)
					r.Delete("/labels/{labelId}", h.DetachLabel)
					r.Get("/metadata", h.ListIssueMetadata)
					r.Put("/metadata/{key}", h.SetIssueMetadataKey)
					r.Delete("/metadata/{key}", h.DeleteIssueMetadataKey)
					r.Put("/properties/{propertyId}", h.SetIssueProperty)
					r.Delete("/properties/{propertyId}", h.DeleteIssueProperty)
					r.Get("/pull-requests", h.ListPullRequestsForIssue)
					r.Post("/pull-requests", h.LinkIssuePullRequest)
					r.Delete("/pull-requests/{prId}", h.UnlinkIssuePullRequest)
					r.Put("/pr-auto-complete", h.SetIssuePRAutoComplete)
				})
			})

			// Task messages (user-facing, not daemon auth)
			r.Get("/api/tasks/{taskId}/messages", h.ListTaskMessagesByUser)

			// Custom issue properties (definitions; values live under /api/issues/{id}/properties)
			r.Route("/api/properties", func(r chi.Router) {
				r.Get("/", h.ListProperties)
				r.Post("/", h.CreateProperty)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", h.GetProperty)
					r.Patch("/", h.UpdateProperty)
				})
			})

			// Plan approvers: members besides owners who may approve plans.
			r.Route("/api/plan-approvers", func(r chi.Router) {
				r.Get("/", h.ListPlanApprovers)
				r.Post("/", h.AddPlanApprover)
				r.Delete("/{userId}", h.RemovePlanApprover)
			})

			// Meeting room: meeting-origin issues and issues carrying a plan.
			r.Get("/api/meetings", h.ListMeetings)

			// Labels
			r.Route("/api/labels", func(r chi.Router) {
				r.Get("/", h.ListLabels)
				r.Post("/", h.CreateLabel)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", h.GetLabel)
					r.Put("/", h.UpdateLabel)
					r.Delete("/", h.DeleteLabel)
				})
			})

			// Issue status catalog (MUL-6243). Reads are open to any member —
			// every client needs the catalog to render a status. Writes are
			// gated to workspace owner/admin inside the handlers.
			r.Route("/api/issue-statuses", func(r chi.Router) {
				r.Get("/", h.ListIssueStatuses)
				r.Post("/", h.CreateIssueStatus)
				r.Patch("/reorder", h.ReorderIssueStatuses)
				r.Route("/{id}", func(r chi.Router) {
					r.Patch("/", h.UpdateIssueStatus)
					r.Delete("/", h.ArchiveIssueStatus)
				})
			})

			// Projects
			r.Route("/api/projects", func(r chi.Router) {
				r.Get("/search", h.SearchProjects)
				r.Get("/", h.ListProjects)
				r.Post("/", h.CreateProject)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", h.GetProject)
					r.Put("/", h.UpdateProject)
					r.Delete("/", h.DeleteProject)
					r.Get("/resources", h.ListProjectResources)
					r.Post("/resources", h.CreateProjectResource)
					r.Put("/resources/{resourceId}", h.UpdateProjectResource)
					r.Delete("/resources/{resourceId}", h.DeleteProjectResource)
					r.Get("/members", h.ListProjectMembers)
					r.Post("/members", h.AddProjectMember)
					r.Delete("/members/{userId}", h.RemoveProjectMember)
				})
			})

			// Project-manager Bots: owner-only management.
			r.Route("/api/bots", func(r chi.Router) {
				r.Use(handler.RequireHumanActor)
				r.Use(middleware.RequireWorkspaceRole(queries, "owner"))
				r.Get("/", h.ListBots)
				r.Post("/", h.CreateBot)
				r.Route("/{botId}", func(r chi.Router) {
					r.Patch("/", h.UpdateBot)
					r.Delete("/", h.DeleteBot)
					r.Post("/token", h.RotateBotToken)
					r.Delete("/token", h.RevokeBotToken)
				})
			})

			// Pins
			r.Route("/api/pins", func(r chi.Router) {
				r.Get("/", h.ListPins)
				r.Post("/", h.CreatePin)
				r.Put("/reorder", h.ReorderPins)
				r.Delete("/{itemType}/{itemId}", h.DeletePin)
			})

			// Saved issue views (MUL-4796).
			r.Get("/api/issue-view-preferences", h.GetIssueViewPreference)
			r.Put("/api/issue-view-preferences", h.PutIssueViewPreference)
			r.Route("/api/issue-views", func(r chi.Router) {
				r.Get("/", h.ListIssueViews)
				r.Post("/", h.CreateIssueView)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", h.GetIssueViewByID)
					r.Patch("/", h.UpdateIssueView)
					r.Delete("/", h.DeleteIssueView)
				})
			})

			// Attachments
			r.Get("/api/attachments/{id}", h.GetAttachmentByID)
			// /api/attachments/{id}/download is registered in the
			// outer Auth-only group above so it can be loaded as a
			// native <img>/<video> src without workspace headers
			// (MUL-3130). The handler self-resolves the workspace
			// from the attachment row.
			r.Get("/api/attachments/{id}/content", h.GetAttachmentContent)
			r.Delete("/api/attachments/{id}", h.DeleteAttachment)

			// Comments
			r.Route("/api/comments/{commentId}", func(r chi.Router) {
				r.With(handler.RequireHumanActor).Get("/sub-issue-preview", h.PreviewCommentSubIssue)
				r.With(handler.RequireHumanActor).Post("/sub-issues", h.CreateCommentSubIssue)
				r.Put("/", h.UpdateComment)
				r.Delete("/", h.DeleteComment)
				// Same handler under a path servers from before #8296 do not
				// route. Clients that promise "replies are kept" call this one,
				// so a request that reaches an older server — mid-rollout, after
				// a rollback, or self-hosted — fails instead of deleting the
				// replies with the comment.
				r.Delete("/keep-replies", h.DeleteComment)
				r.Post("/resolve", h.ResolveComment)
				r.Delete("/resolve", h.UnresolveComment)
				r.Post("/reactions", h.AddReaction)
				r.Delete("/reactions", h.RemoveReaction)
			})

			// Agents
			r.Route("/api/agents", func(r chi.Router) {
				r.Get("/", h.ListAgents)
				r.Post("/", h.CreateAgent)
				// The workspace's built-in Chief of Staff. Server-owned: the
				// caller supplies only a runtime and a language, so a client
				// cannot mint an agent carrying `system_key` and thereby claim
				// the system instruction layer. Idempotent per workspace.
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", h.GetAgent)
					r.Put("/", h.UpdateAgent)
					r.Post("/archive", h.ArchiveAgent)
					r.Post("/restore", h.RestoreAgent)
					r.Post("/cancel-tasks", h.CancelAgentTasks)
					r.Get("/tasks", h.ListAgentTasks)
					r.Get("/labels", h.ListLabelsForAgent)
					r.Post("/labels", h.AttachLabelToAgent)
					r.Delete("/labels/{labelId}", h.DetachLabelFromAgent)
					// Workspace MCP servers assigned to this agent. A library
					// entry does nothing until it is added here, and the
					// binding carries its own enabled toggle.
					r.Get("/mcp-servers", h.ListAgentMcpServers)
					r.Post("/mcp-servers", h.AddAgentMcpServer)
					r.Put("/mcp-servers/{serverId}/enabled", h.SetAgentMcpServerEnabled)
					r.Delete("/mcp-servers/{serverId}", h.RemoveAgentMcpServer)
					// Dedicated env-management endpoint. Admits the agent
					// owner or a workspace owner/admin; agent actors are
					// denied. Every reveal / write is audited to
					// activity_log. See MUL-2600, MUL-5438 and
					// internal/handler/agent_env.go.
					r.Get("/env", h.GetAgentEnv)
					r.Put("/env", h.UpdateAgentEnv)
				})
			})

			// Dashboard — workspace-wide token + run-time rollups for the
			// "/{slug}/dashboard" page. Optional ?project_id filter scopes
			// the rollup to a single project.
			r.Route("/api/dashboard", func(r chi.Router) {
				r.Get("/usage/daily", h.GetDashboardUsageDaily)
				r.Get("/usage/by-agent", h.GetDashboardUsageByAgent)
				r.Get("/agent-runtime", h.GetDashboardAgentRunTime)
				r.Get("/runtime/daily", h.GetDashboardRunTimeDaily)
				r.Get("/failures/daily", h.GetDashboardFailuresDaily)
				r.Get("/failures/by-agent", h.GetDashboardFailuresByAgent)
			})

			// Runtimes
			r.Route("/api/runtimes", func(r chi.Router) {
				r.Get("/", h.ListAgentRuntimes)
				r.Route("/{runtimeId}", func(r chi.Router) {
					r.Patch("/", h.UpdateAgentRuntime)
					r.Get("/usage", h.GetRuntimeUsage)
					r.Get("/usage/by-agent", h.GetRuntimeUsageByAgent)
					r.Get("/usage/by-hour", h.GetRuntimeUsageByHour)
					r.Get("/activity", h.GetRuntimeTaskActivity)
					r.Post("/update", h.InitiateUpdate)
					r.Get("/update/{updateId}", h.GetUpdate)
					r.Delete("/", h.DeleteAgentRuntime)
					// Confirmed variant of DELETE: unbind every agent bound to
					// this runtime (they keep their configuration and chats and
					// need a new runtime to run again), cancel their tasks,
					// detach their task history, then delete the runtime — all
					// in one transaction. Used by the DeleteRuntimeDialog when
					// the strict DELETE refused with
					// `runtime_has_active_agents` and the user confirmed.
					r.Post("/unbind-agents-and-delete", h.UnbindAgentsAndDeleteRuntime)
					// Legacy path for installed clients built against the
					// archive-and-delete contract (MUL-5559 renamed the
					// behaviour, not just the route). Same handler.
					r.Post("/archive-agents-and-delete", h.UnbindAgentsAndDeleteRuntime)
				})
			})

			// Tasks (user-facing, with ownership check)
			r.Post("/api/tasks/{taskId}/cancel", h.CancelTaskByUser)

			// Workspace-wide agent task snapshot for presence derivation:
			// every active task + each agent's most recent terminal task.
			r.Get("/api/agent-task-snapshot", h.ListWorkspaceAgentTaskSnapshot)

			// Independent workspace-level list backing the issues-header
			// "agents working" chip and its assignee-id Table filter.
			r.Get("/api/working-agents", h.ListWorkspaceWorkingAgents)

			// Workspace-wide daily agent activity (last 30d, anchored on
			// completed_at). Backs the Agents-list sparkline (trailing 7d
			// slice) AND the agent detail "Last 30 days" panel.
			r.Get("/api/agent-activity-30d", h.GetWorkspaceAgentActivity30d)

			// Workspace-wide 30-day run counts per agent for the Agents-list RUNS column.
			r.Get("/api/agent-run-counts", h.GetWorkspaceAgentRunCounts)

			r.Route("/api/chat/sessions", func(r chi.Router) {
				r.Post("/", h.CreateChatSession)
				r.Get("/", h.ListChatSessions)
				r.Route("/{sessionId}", func(r chi.Router) {
					r.Get("/", h.GetChatSession)
					r.Patch("/", h.UpdateChatSession)
					r.Patch("/pin", h.SetChatSessionPinned)
					r.Patch("/archive", h.SetChatSessionArchived)
					r.Delete("/", h.DeleteChatSession)
					r.Post("/messages", h.SendChatMessage)
					r.Get("/messages", h.ListChatMessages)
					r.Get("/messages/page", h.ListChatMessagesPage)
					r.Get("/pending-task", h.GetPendingChatTask)
					r.Delete("/queued-tasks", h.ClearQueuedChatTasks)
					r.Post("/queued-tasks/{taskId}/prioritize", h.PrioritizeQueuedChatTask)
					r.Post("/read", h.MarkChatSessionRead)
					// Deferred-cancellation draft restores (#5219):
					// creator-only fetch + idempotent consume.
					r.Get("/draft-restores", h.ListChatDraftRestores)
					r.Delete("/draft-restores/{restoreId}", h.ConsumeChatDraftRestore)
				})
			})
			r.Get("/api/chat/pending-tasks", h.ListPendingChatTasks)
			r.Get("/api/chat/pending-tasks/has-any", h.HasPendingChatTasks)

			// Quick-agent bar: per-user pinned agents for one-tap new chats.
			r.Get("/api/chat/pinned-agents", h.ListChatPinnedAgents)
			r.Post("/api/chat/pinned-agents", h.PinChatAgent)
			r.Delete("/api/chat/pinned-agents/{agentId}", h.UnpinChatAgent)

			// Agent-facing transcript read (MUL-3871). The caller's task-scoped
			// token resolves to its own chat session; no session id is passed, so
			// an agent can only read its own conversation.
			r.Get("/api/chat/history", h.GetChatHistory)

			// Inbox
			r.Route("/api/inbox", func(r chi.Router) {
				r.Get("/", h.ListInbox)
				// Archived notifications, for the inbox's "Archived" sub-view.
				// Separate from "/" so the main list keeps its contract and
				// never carries the unbounded archive.
				r.Get("/archived", h.ListArchivedInbox)
				r.Get("/archived/page", h.ListArchivedInboxPage)
				r.Get("/archived/facets", h.GetArchivedInboxFacets)
				r.Get("/unread-count", h.CountUnreadInbox)
				// Cross-workspace unread summary: account-level, keyed on the
				// user. Backs the workspace-switcher dot for OTHER workspaces.
				r.Get("/unread-summary", h.UnreadInboxSummary)
				r.Post("/mark-all-read", h.MarkAllInboxRead)
				r.Post("/archive-all", h.ArchiveAllInbox)
				r.Post("/archive-all-read", h.ArchiveAllReadInbox)
				r.Post("/archive-completed", h.ArchiveCompletedInbox)
				r.Post("/{id}/read", h.MarkInboxRead)
				r.Post("/{id}/unread", h.MarkInboxUnread)
				r.Post("/{id}/archive", h.ArchiveInboxItem)
				r.Post("/{id}/unarchive", h.UnarchiveInboxItem)
			})

			// Notification preferences
			r.Route("/api/notification-preferences", func(r chi.Router) {
				r.Get("/", h.GetNotificationPreferences)
				r.Patch("/", h.PatchNotificationPreferences)
				r.Put("/", h.UpdateNotificationPreferences)
			})
		})
	})

	return r, h
}

// membershipChecker implements realtime.MembershipChecker using database queries.
type membershipChecker struct {
	queries *db.Queries
}

func (mc *membershipChecker) IsMember(ctx context.Context, userID, workspaceID string) bool {
	_, err := mc.queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      parseUUID(userID),
		WorkspaceID: parseUUID(workspaceID),
	})
	return err == nil
}

// patResolver implements realtime.PATResolver using database queries.
// patCache is shared with the Auth and DaemonAuth middlewares so a token
// revoke through any path invalidates the cache for all of them. Nil
// cache is supported and degrades to direct DB lookups.
type patResolver struct {
	queries *db.Queries
	cache   *auth.PATCache
}

func (pr *patResolver) ResolveToken(ctx context.Context, token string) (string, bool) {
	hash := auth.HashToken(token)

	if userID, ok := pr.cache.Get(ctx, hash); ok {
		return userID, true
	}

	pat, err := pr.queries.GetPersonalAccessTokenByHash(ctx, hash)
	if err != nil {
		return "", false
	}

	userID := util.UUIDToString(pat.UserID)

	var expiresAt time.Time
	if pat.ExpiresAt.Valid {
		expiresAt = pat.ExpiresAt.Time
	}
	pr.cache.Set(ctx, hash, userID, auth.TTLForExpiry(time.Now(), expiresAt))

	// Cache miss = first WS auth in this TTL window. Refresh last_used_at;
	// subsequent connects within the window skip the write.
	go pr.queries.UpdatePersonalAccessTokenLastUsed(context.Background(), pat.ID)

	return userID, true
}

// parseUUID is a thin alias for util.MustParseUUID. Call sites here are all
// internal round-trips of DB-sourced UUIDs (e.g. issue.ID, e.ActorID), so an
// invalid value indicates a programming error and should panic loudly.
func parseUUID(s string) pgtype.UUID {
	return util.MustParseUUID(s)
}

// optionalUUID returns a NULL pgtype.UUID for an empty string and otherwise
// behaves like parseUUID. Use this for actor IDs on events where the producer
// may legitimately be a "system" actor with no member/agent attribution
// (e.g. GitHub webhook auto-status sync) — the activity_log and inbox_item
// tables both allow actor_id to be NULL.
func optionalUUID(s string) pgtype.UUID {
	if s == "" {
		return pgtype.UUID{}
	}
	return util.MustParseUUID(s)
}

func splitAndTrim(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	res := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			res = append(res, trimmed)
		}
	}
	return res
}
