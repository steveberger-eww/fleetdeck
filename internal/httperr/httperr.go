// Package httperr carries an HTTP status with an error message, for errors the user should see verbatim.
package httperr

import "errors"

type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

// New returns an error that the HTTP layer answers with status and {"error": msg}.
func New(status int, msg string) error { return &Error{status, msg} }

// Bad is New(400, msg).
func Bad(msg string) error { return &Error{400, msg} }

// Status returns the HTTP status for err: its own for an *Error, 500 otherwise.
func Status(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 500
}
