package svcerr

import (
	"encoding/json"
	"errors"
	"net/http"
)

// This file is OPTIONAL. Nothing else in the package depends on it. It is one
// default for a new handler that has no error envelope of its own; an app with
// an existing wire shape should keep it and use only Error, StatusFor and
// CodeFor.

// Envelope is the JSON body [WriteJSON] writes. Its shape matches Tether's
// existing {"error":{"code","message"}} nesting.
type Envelope struct {
	Error EnvelopeError `json:"error"`
}

// EnvelopeError is the object inside [Envelope].
type EnvelopeError struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

// WriteJSON writes err as an [Envelope]. For an *Error anywhere in err's chain
// it uses that error's status, code, safe message and field. For any other error
// it writes fallback as the status and a generic body, and never puts the
// error's own text in it: the response says "internal error" under
// CodeInternal, with the status the caller chose. An empty Message falls back
// to the status text. A nil err is treated as an internal error.
func WriteJSON(w http.ResponseWriter, err error, fallback int) {
	status := fallback
	if !validStatus(status) {
		status = http.StatusInternalServerError
	}
	env := Envelope{Error: EnvelopeError{Code: CodeInternal, Message: "internal error"}}

	var e *Error
	if errors.As(err, &e) && e != nil {
		status = e.status()
		env.Error = EnvelopeError{Code: e.Code, Message: e.Message, Field: e.Field}
		if env.Error.Message == "" {
			env.Error.Message = http.StatusText(status)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}
