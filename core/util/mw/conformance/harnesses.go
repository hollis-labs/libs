package conformance

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"

	"github.com/hollis-labs/libs/core/util/mw"
	"github.com/hollis-labs/libs/core/util/mw/httpmw"
	"github.com/hollis-labs/libs/core/util/mw/jobmw"
	"github.com/hollis-labs/libs/core/util/svcerr"
)

// --- generic mw.Handler[string,string] ---

// Generic is the harness for plain mw.Handler chains.
type Generic struct{}

// Name identifies the harness.
func (Generic) Name() string { return "generic" }

// CommittedResponse reports whether the response is committed once the handler ran.
func (Generic) CommittedResponse() bool { return false }

// Run assembles the probes behind a counting handler and sends one request.
func (Generic) Run(probes []Probe, deny bool) (Result, error) {
	var res Result
	members := []mw.Member[string, string]{mw.RecoverMember[string, string]()}
	for _, p := range probes {
		members = append(members, mw.Member[string, string]{
			Desc: p.Desc, Mode: p.Mode,
			MW: func(next mw.Handler[string, string]) mw.Handler[string, string] {
				return func(ctx context.Context, r string) (string, error) {
					res.Trace = append(res.Trace, p.Desc.Name+">")
					defer func() { res.Trace = append(res.Trace, "<"+p.Desc.Name) }()
					switch p.Does {
					case DenyIfAsked:
						if r == "deny" {
							return "", errors.New("denied")
						}
					case PanicBefore:
						panic("probe")
					case PanicAfter:
						out, err := next(ctx, r)
						_ = out
						_ = err
						panic("probe")
					}
					return next(ctx, r)
				}
			},
		})
	}
	s, err := mw.Assemble(mw.Options{Observer: Collect(&res.Events)}, members...)
	if err != nil {
		return res, err
	}
	in := "ok"
	if deny {
		in = "deny"
	}
	_, err = s.Wrap(func(context.Context, string) (string, error) { res.HandlerCalls++; return "ok", nil })(context.Background(), in)
	res.Failed = err != nil
	return res, nil
}

// --- job adapter ---

// Job is the harness for job handler chains.
type Job struct{}

// Name identifies the harness.
func (Job) Name() string { return "job" }

// CommittedResponse reports whether the response is committed once the handler ran.
func (Job) CommittedResponse() bool { return false }

// Run assembles the probes behind a counting handler and sends one job.
func (Job) Run(probes []Probe, deny bool) (Result, error) {
	var res Result
	members := []jobmw.Member{mw.RecoverMember[jobmw.Job, struct{}]()}
	for _, p := range probes {
		members = append(members, jobmw.Member{
			Desc: p.Desc, Mode: p.Mode,
			MW: func(next jobmw.Handler) jobmw.Handler {
				return func(ctx context.Context, j jobmw.Job) (struct{}, error) {
					res.Trace = append(res.Trace, p.Desc.Name+">")
					defer func() { res.Trace = append(res.Trace, "<"+p.Desc.Name) }()
					switch p.Does {
					case DenyIfAsked:
						if j.Type == "deny" {
							return struct{}{}, errors.New("denied")
						}
					case PanicBefore:
						panic("probe")
					case PanicAfter:
						_, _ = next(ctx, j)
						panic("probe")
					}
					return next(ctx, j)
				}
			},
		})
	}
	s, err := mw.Assemble(mw.Options{Observer: Collect(&res.Events)}, members...)
	if err != nil {
		return res, err
	}
	j := jobmw.Job{Type: "ok"}
	if deny {
		j.Type = "deny"
	}
	_, err = s.Wrap(func(context.Context, jobmw.Job) (struct{}, error) { res.HandlerCalls++; return struct{}{}, nil })(context.Background(), j)
	res.Failed = err != nil
	return res, nil
}

// --- net/http adapter ---

// HTTP is the harness for net/http chains.
type HTTP struct{}

// Name identifies the harness.
func (HTTP) Name() string { return "http" }

// CommittedResponse reports whether the response is committed once the handler ran.
func (HTTP) CommittedResponse() bool { return true }

// Run assembles the probes behind a counting handler and sends one request.
func (HTTP) Run(probes []Probe, deny bool) (Result, error) {
	var res Result
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	members := []httpmw.Member{httpmw.Recover(quiet)}
	for _, p := range probes {
		members = append(members, httpmw.Member{
			Desc: p.Desc, Mode: p.Mode,
			MW: func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					res.Trace = append(res.Trace, p.Desc.Name+">")
					defer func() { res.Trace = append(res.Trace, "<"+p.Desc.Name) }()
					switch p.Does {
					case DenyIfAsked:
						if r.Header.Get("X-Deny") != "" {
							httpmw.WriteError(w, svcerr.New(svcerr.CodePermission, "denied"))
							return
						}
					case PanicBefore:
						panic("probe")
					case PanicAfter:
						next.ServeHTTP(w, r)
						panic("probe")
					}
					next.ServeHTTP(w, r)
				})
			},
		})
	}
	s, err := httpmw.Assemble(mw.Options{Observer: Collect(&res.Events)}, members...)
	if err != nil {
		return res, err
	}
	req := httptest.NewRequest("GET", "/", nil)
	if deny {
		req.Header.Set("X-Deny", "1")
	}
	rr := httptest.NewRecorder()
	s.Then(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { res.HandlerCalls++; io.WriteString(w, "ok") })).ServeHTTP(rr, req)
	res.Failed = rr.Code >= 400
	return res, nil
}
