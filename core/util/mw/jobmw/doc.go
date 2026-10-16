// Package jobmw is the queue-job adapter for the mw contract. A job handler is
// mw.Handler[Job, struct{}], so recover, timeout and tracing members from mw
// compose with the job members here.
//
// Middleware never touches queue state. It reports what should happen to the
// job by returning typed errors (ReleaseError, DropError, RetryError,
// PermanentError); Resolve turns the result into an Action for the worker,
// which acts on it under its own fencing token.
//
// The members are RateLimited, WithoutOverlapping, UniqueByKey,
// ThrottleOnException and Classify (attempt budget and backoff). Counters and
// locks sit behind the Limiter, Locker and Counter interfaces; the in-memory
// implementations are for tests and single-process use. A store error is a
// mw.Fault on a closed member, so a job is not run unthrottled because a store
// was down.
package jobmw
