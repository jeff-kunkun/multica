package llm

import (
	"errors"
	"strconv"

	openai "github.com/openai/openai-go/v3"
)

// StatusError reports the HTTP status an upstream call came back with, in a
// shape a caller can read without importing the OpenAI SDK.
//
// This exists because the SDK carries the status as a STRUCT FIELD
// (openai.Error.StatusCode), and a field cannot be reached through an
// interface. Any package outside this one that wanted to branch on 401 vs 500
// would therefore have to import the SDK — which the single-entry-point rule
// in this package's doc comment forbids — or, worse, declare an interface the
// SDK error silently fails to satisfy and end up with a branch that is dead in
// production while its tests pass against a hand-rolled error. The routing
// breaker shipped with exactly that bug (DENE-633 review, F1); making the
// status part of THIS package's error contract is what removes the trap.
//
// Every failure returned by Chat (and therefore by GenerateText and
// GenerateJSON) that carries an upstream status is wrapped in one of these.
// The SDK error stays reachable through errors.As, so existing callers that
// inspect *openai.Error are unaffected.
type StatusError struct {
	// Code is the upstream HTTP status.
	Code int
	// Err is the original error, kept for message and unwrapping.
	Err error
}

func (e *StatusError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "llm: upstream returned " + strconv.Itoa(e.Code)
}

func (e *StatusError) Unwrap() error { return e.Err }

// Status reports the upstream HTTP status. The method name and signature are
// the contract consumers match on with errors.As or a local interface.
func (e *StatusError) Status() int { return e.Code }

// HTTPStatus reports the upstream HTTP status carried by err, if any. It reads
// both the wrapper above and a bare SDK error, so it is correct on an error
// that has not been through Chat.
func HTTPStatus(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	var wrapped *StatusError
	if errors.As(err, &wrapped) && wrapped.Code != 0 {
		return wrapped.Code, true
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) && apiErr.StatusCode != 0 {
		return apiErr.StatusCode, true
	}
	return 0, false
}

// withStatus wraps an SDK error so its status survives as behaviour rather
// than as a field only this package can see. Errors with no upstream status
// (transport failures, context deadlines) are returned untouched.
func withStatus(err error) error {
	if err == nil {
		return nil
	}
	var wrapped *StatusError
	if errors.As(err, &wrapped) {
		return err
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) && apiErr.StatusCode != 0 {
		return &StatusError{Code: apiErr.StatusCode, Err: err}
	}
	return err
}
