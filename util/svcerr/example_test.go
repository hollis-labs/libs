package svcerr_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/hollis-labs/libs/util/svcerr"
)

func ExampleNew() {
	err := svcerr.New(svcerr.CodeInvalid, "title must not be empty", svcerr.WithField("title"))
	fmt.Println(err)
	// Output: invalid: title must not be empty (field=title)
}

func ExampleWrap() {
	cause := errors.New("sql: no rows in result set") // logged server-side, never sent
	err := svcerr.Wrap(cause, svcerr.CodeNotFound, "no such task")
	fmt.Println(err.Message, errors.Is(err, cause))
	// Output: no such task true
}

func ExampleStatusFor() {
	err := fmt.Errorf("handler: %w", svcerr.New(svcerr.CodeConflict, "version clash"))
	fmt.Println(svcerr.StatusFor(err, http.StatusInternalServerError))
	fmt.Println(svcerr.StatusFor(errors.New("not found"), http.StatusInternalServerError)) // text is never inspected
	// Output:
	// 409
	// 500
}

func ExampleCodeFor() {
	err := svcerr.New(svcerr.CodePermission, "not yours")
	fmt.Println(svcerr.CodeFor(err), svcerr.CodeFor(errors.New("plain")) == "")
	// Output: permission true
}

func ExampleError_Is() {
	err := fmt.Errorf("repo: %w", svcerr.New(svcerr.CodeNotFound, "no such task"))
	fmt.Println(errors.Is(err, svcerr.ErrNotFound), errors.Is(err, svcerr.ErrConflict))
	// Output: true false
}

func ExampleWithStatus() {
	err := svcerr.New("approval_required", "an operator must approve this", svcerr.WithStatus(http.StatusPaymentRequired))
	fmt.Println(svcerr.StatusFor(err, 500))
	// Output: 402
}

func ExampleWriteJSON() {
	rec := httptest.NewRecorder()
	svcerr.WriteJSON(rec, svcerr.New(svcerr.CodeNotFound, "no such task"), http.StatusInternalServerError)
	fmt.Print(rec.Code, " ", rec.Body.String())
	// Output: 404 {"error":{"code":"not_found","message":"no such task"}}
}
