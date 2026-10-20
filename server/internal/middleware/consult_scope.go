package middleware

import "net/http"

// ConsultTaskKind is agent_task_queue.context.type of a consult run
// (DENE-1721): a strong seat answering one question another run asked. The
// answer is its final output, which the daemon reports with its own
// credential, so the run's task token needs no write at all and gets none.
// The prompt says "only read"; the credential is what makes it true. Local
// side effects inside the run's empty workdir stay out of reach.
const ConsultTaskKind = "consult"

func isConsultReadOnlyScope(taskKind string) bool {
	return taskKind == ConsultTaskKind
}

// isReadOnlyMethod reports whether an HTTP method cannot change server state,
// and is therefore allowed for a read-only token.
//
// HEAD and OPTIONS are included because they are defined as safe by RFC 9110
// and are what clients and probes send before a GET. Everything else is
// refused: this predicate is an allowlist on purpose, so a method nobody
// thought about fails closed instead of being treated as a read.
func isReadOnlyMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}
