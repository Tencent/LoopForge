// Package apperr lets application services report failures by kind without
// depending on HTTP. Transport layers map each Kind to their own status.
package apperr

import (
	"errors"
	"fmt"
)

type Kind int

const (
	Invalid Kind = iota + 1
	NotFound
	Conflict
	Forbidden
	// Unavailable means a required dependency is not configured.
	Unavailable
	// Upstream means a dependency was configured but failed.
	Upstream
)

// Error carries a user-facing message plus optional extra response fields.
type Error struct {
	Kind    Kind
	Message string
	Fields  map[string]any
}

func (e *Error) Error() string { return e.Message }

func New(kind Kind, message string) *Error { return &Error{Kind: kind, Message: message} }

func Newf(kind Kind, format string, args ...any) *Error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

// With attaches an extra response field, e.g. the id of a conflicting item.
func (e *Error) With(key string, value any) *Error {
	if e.Fields == nil {
		e.Fields = map[string]any{}
	}
	e.Fields[key] = value
	return e
}

// As extracts an *Error from err's chain.
func As(err error) (*Error, bool) {
	var target *Error
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}
