package jobmw_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hollis-labs/libs/core/util/mw"
	"github.com/hollis-labs/libs/core/util/mw/jobmw"
)

func runChain(h jobmw.Handler, j jobmw.Job, members ...jobmw.Member) error {
	stack, err := mw.Assemble(mw.Options{}, members...)
	if err != nil {
		return err
	}
	_, err = stack.Wrap(h)(context.Background(), j)
	return err
}

func ExampleRateLimited() {
	ok := func(context.Context, jobmw.Job) (struct{}, error) { return struct{}{}, nil }
	limiter := &jobmw.MemLimiter{}
	member := jobmw.RateLimited(limiter, jobmw.ByType, 1, time.Minute)
	job := jobmw.Job{ID: "1", Type: "send-email"}

	fmt.Println(runChain(ok, job, member)) // first run is admitted
	action, after := jobmw.Resolve(runChain(ok, job, member))
	fmt.Println(action, after > 0) // the second is released, not failed
	// Output:
	// <nil>
	// release true
}

func ExampleClassify() {
	policy := jobmw.RetryPolicy{
		Tries:   3,
		Backoff: []time.Duration{500 * time.Millisecond, 2 * time.Second}, // sub-second delays are kept
	}
	fail := func(context.Context, jobmw.Job) (struct{}, error) { return struct{}{}, errors.New("upstream 503") }

	for attempt := 1; attempt <= 3; attempt++ {
		err := runChain(fail, jobmw.Job{Attempt: attempt}, jobmw.Classify(policy))
		action, after := jobmw.Resolve(err)
		fmt.Println(attempt, action, after)
	}
	// Output:
	// 1 retry 500ms
	// 2 retry 2s
	// 3 fail 0s
}
