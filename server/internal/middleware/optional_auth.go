package middleware

import (
	"net/http"

	"github.com/multica-ai/multica/server/internal/auth"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// OptionalAuth authenticates a request when a credential is present and stamps
// the server-set identity headers exactly as Auth does, but never rejects:
// a missing or invalid credential falls through to the next handler with no
// identity headers set, so a route can still authorise the request by some
// other credential (e.g. a signed capability in the query string).
//
// It exists for the public attachment preview route, where a valid signed
// capability must redeem the request without a session, but a session — if the
// caller has one — must be honoured with the same validation the /download
// route applies. Credential validation is not reimplemented here: the request
// is run through Auth itself against a discard writer, and its standard error
// response is dropped on failure. auth.go therefore stays byte-identical to the
// auth_required path and the two routes cannot drift.
//
// Identity headers are server-set only. OptionalAuth strips every client
// supplied one before validation, because unlike Auth it does not overwrite
// them when the request turns out to be anonymous — leaving them in place would
// let a client mint X-User-ID and read whatever that user can.
func OptionalAuth(queries *db.Queries, patCache *auth.PATCache, cloudPAT *auth.CloudPATVerifier, cfSigner *auth.CloudFrontSigner) func(http.Handler) http.Handler {
	required := Auth(queries, patCache, cloudPAT, cfSigner)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Server-set only. Auth re-stamps all of these on success; on the
			// anonymous path below nothing restores them, so a client value
			// must not survive this function. X-User-Email is included for
			// symmetry with the JWT branch even though no handler reads it.
			r.Header.Del("X-User-ID")
			r.Header.Del("X-User-Email")
			r.Header.Del("X-Actor-Source")
			r.Header.Del("X-Agent-ID")
			r.Header.Del("X-Task-ID")

			// Run Auth as a probe. Auth signals acceptance by calling the
			// handler it wraps and rejection by writing an error and stopping,
			// so success is exactly "the probe handler was invoked". All its
			// output — the rejection body, and any Set-Cookie written by
			// renewCookieSession before the call on success — lands on the
			// probe writer and is only committed on success.
			var resolved *http.Request
			probe := &authProbeWriter{header: http.Header{}}
			required(http.HandlerFunc(func(_ http.ResponseWriter, probed *http.Request) {
				resolved = probed
			})).ServeHTTP(probe, r)

			if resolved == nil {
				// Missing or invalid credential. Continue anonymous: the route
				// decides what that means. This also keeps a stale cookie from
				// breaking a signed capability link the caller was given.
				next.ServeHTTP(w, r)
				return
			}

			// Authenticated. Commit session-renewal cookies Auth set while
			// deciding, then serve with the identity-stamped request (and the
			// renewed context when the session was refreshed).
			for k, vs := range probe.header {
				for _, v := range vs {
					w.Header().Add(k, v)
				}
			}
			next.ServeHTTP(w, resolved)
		})
	}
}

// authProbeWriter captures whatever the Auth middleware writes while it decides
// a request's identity, so OptionalAuth can discard the standard rejection
// response and fall through to the route's own credential check. Header writes
// are kept (they carry session-renewal Set-Cookie on success); status and body
// writes are dropped because they only ever express the rejection.
type authProbeWriter struct {
	header http.Header
}

func (p *authProbeWriter) Header() http.Header         { return p.header }
func (p *authProbeWriter) WriteHeader(_ int)           {}
func (p *authProbeWriter) Write(b []byte) (int, error) { return len(b), nil }
