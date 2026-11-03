package migration

import (
	"errors"

	"github.com/openeverest/plugin-migrations/backend/internal/engine"
)

type ErrorKind string

const (
	KindInvalid     ErrorKind = "invalid"
	KindNotFound    ErrorKind = "not-found"
	KindConflict    ErrorKind = "conflict"
	KindUnavailable ErrorKind = "unavailable"
)

// Error messages are safe to return to API clients.
type Error struct {
	Kind    ErrorKind
	Message string
}

func (e *Error) Error() string { return e.Message }

func newError(kind ErrorKind, message string) error {
	return &Error{Kind: kind, Message: message}
}

// KindOf classifies service and engine errors; unclassified errors are internal.
func KindOf(err error) ErrorKind {
	var migrationErr *Error
	if errors.As(err, &migrationErr) {
		return migrationErr.Kind
	}
	switch engine.KindOf(err) {
	case engine.KindInvalidRequest:
		return KindInvalid
	case engine.KindConflict:
		return KindConflict
	case engine.KindUnavailable:
		return KindUnavailable
	}
	return ""
}
