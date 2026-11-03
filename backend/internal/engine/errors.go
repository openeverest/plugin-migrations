package engine

import "errors"

// Kind classifies engine errors so the API layer can choose a status code
// without parsing messages.
type Kind string

const (
	// KindInvalidRequest: the caller's input cannot be used (HTTP 400).
	KindInvalidRequest Kind = "invalid-request"
	// KindConflict: the worker rejected the action in its current state (HTTP 409).
	KindConflict Kind = "conflict"
	// KindUnavailable: the worker could not be reached or answered badly (HTTP 502).
	KindUnavailable Kind = "unavailable"
)

// Error messages are safe to return to clients: they never echo credentials.
type Error struct {
	Kind    Kind
	Message string
}

func (e *Error) Error() string { return e.Message }

// KindOf returns the error's Kind, or an empty Kind for unclassified errors.
func KindOf(err error) Kind {
	var engineErr *Error
	if errors.As(err, &engineErr) {
		return engineErr.Kind
	}
	return ""
}
