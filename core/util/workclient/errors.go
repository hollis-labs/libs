package workclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Sentinels an *Error or a transport failure matches under errors.Is.
var (
	ErrNotFound     = errors.New("workclient: not found")
	ErrUnauthorized = errors.New("workclient: the service did not accept the key")
	ErrForbidden    = errors.New("workclient: forbidden")
	ErrUnavailable  = errors.New("workclient: the service cannot do that now")
	// ErrUnreachable is a request that got no answer.
	ErrUnreachable = errors.New("workclient: the Work service could not be reached")
	// ErrBadAnswer is an answer that could not be read or is not the JSON expected.
	ErrBadAnswer = errors.New("workclient: the Work service's answer could not be read")
)

// Conflict is an edit made from a stale revision. The service kept it as Suggestions
// suggestions against the item's current revision Rev.
type Conflict struct {
	Rev         int
	Suggestions int
}

// OpenNotes is a sign-off refused because round Round still has Open open notes.
type OpenNotes struct {
	Round int
	Open  int
}

// Error is a non-2xx answer from the service.
type Error struct {
	Status  int
	Code    string
	Message string
	// Body is the answer's JSON object, as the service sent it (nil if it was not one).
	Body map[string]any
	// Conflict is set for a stale edit (409 with "conflict": true).
	Conflict *Conflict
	// OpenNotes is set for a sign-off refused over open notes (409 with "open").
	OpenNotes *OpenNotes
	// Task is the Torque task SendToTorque made before something after it failed.
	Task string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("workclient: %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("workclient: %d %s", e.Status, http.StatusText(e.Status))
}

// Is matches the sentinel for the answer's status.
func (e *Error) Is(target error) bool {
	switch target {
	case ErrNotFound:
		return e.Status == http.StatusNotFound
	case ErrUnauthorized:
		return e.Status == http.StatusUnauthorized
	case ErrForbidden:
		return e.Status == http.StatusForbidden
	case ErrUnavailable:
		return e.Status == http.StatusServiceUnavailable
	}
	return false
}

// answerError reads a non-2xx answer.
func answerError(status int, raw []byte) *Error {
	e := &Error{Status: status}
	var body map[string]any
	if json.Unmarshal(raw, &body) == nil {
		e.Body = body
	}
	e.Code, _ = body["code"].(string)
	e.Message, _ = body["message"].(string)
	if e.Message == "" && body == nil {
		e.Message = strings.TrimSpace(string(raw))
	}
	e.Task, _ = body["task"].(string)
	num := func(k string) int {
		f, _ := body[k].(float64)
		return int(f)
	}
	if status == http.StatusConflict {
		switch {
		case body["conflict"] == true:
			e.Conflict = &Conflict{Rev: num("rev"), Suggestions: num("suggestions")}
		case body["open"] != nil:
			e.OpenNotes = &OpenNotes{Round: num("round"), Open: num("open")}
		}
	}
	return e
}

// wrapped is a failure that matches a sentinel and keeps its cause.
type wrapped struct {
	kind  error
	cause error
}

func (w *wrapped) Error() string {
	if w.cause == nil {
		return w.kind.Error()
	}
	return w.kind.Error() + ": " + w.cause.Error()
}

func (w *wrapped) Unwrap() []error {
	if w.cause == nil {
		return []error{w.kind}
	}
	return []error{w.kind, w.cause}
}
