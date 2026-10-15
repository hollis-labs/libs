// Package tesseracttest is a fake Tesseract for testing code that uses the
// tesseract client without a running daemon: an httptest server that answers
// the routes the client calls (recall, knowledge current-by-key, revision by
// id, deprecate, namespace listing, readiness) from an in-memory set of
// revisions, counts and records every request, and has knobs to make routes
// fail, demand a bearer token, cap page sizes and delay answers.
//
// It speaks the HTTP door's shapes, mirrored from the server: recall returns
// {results, manifest} with results_total and next_cursor; a trailing /* in a
// namespace expands to its sub-namespaces but does not include the namespace
// itself; the recall body and its nested filters object are decoded strictly
// (unknown fields are a 400 validation_error, and filter keys are Go field
// names, not snake_case); a missing key or revision is a 404 carrying
// {code: not_found}; deprecated revisions are omitted from recall but still
// readable by id.
//
// It validates the request but does not apply recall filters (statuses, tags,
// confidence, time bounds): a test that needs filtering should inspect
// Fake.Requests or load only the revisions it wants.
package tesseracttest
