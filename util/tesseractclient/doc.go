// Package tesseract is a small, shared HTTP client for Tesseract's memory API.
//
// It covers only the operations Station's and Tangent's independent,
// hand-rolled clients call today: recall (Client.Recall, Client.RecallAll),
// one write (Client.Deprecate), two point-reads (Client.GetCurrent,
// Client.GetRevision), namespace listing (Client.ListNamespaces) and a health
// probe (Client.Health). It speaks Tesseract's HTTP door only; it does not
// wrap the MCP tool surface, retry, or back off.
//
// # Wire shapes
//
// The HTTP door's request shapes differ from the MCP tools': fields are real
// JSON values rather than JSON-encoded strings, and the recall route rejects
// any field it does not know (it decodes with DisallowUnknownFields). The one
// object that is not snake_case is the nested `filters` object: the server
// decodes it into memory.RecallFilters, a struct carrying no JSON tags, so its
// keys are Go field names (Statuses, Tags, ConfidenceMin, ...). RecallFilters
// spells them exactly and carries only fields the server accepts.
//
// Recall goes through POST /v1/memory/recall rather than GET /v1/recall
// because only the POST route returns a manifest with results_total and
// next_cursor and honors a cursor.
//
// # Errors
//
// A non-2xx answer is an *APIError carrying Tesseract's own code and message;
// a 404 or a not_found code matches ErrNotFound under errors.Is. A transport
// failure, an unreadable or oversize body, or an undecodable 2xx body matches
// ErrUnavailable. Other statuses, including 5xx, are neither: they are
// Tesseract answering.
//
// # Compatibility
//
// Tesseract's HTTP surface is pre-1.0 and can change in any minor release.
// This package's compatibility promise tracks that reality; see the README.
package tesseract
