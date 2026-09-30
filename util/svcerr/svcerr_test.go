package svcerr

import "testing"

func TestHello(t *testing.T) {
	got := Hello()
	want := "hello from svcerr"
	if got != want {
		t.Errorf("Hello() = %q, want %q", got, want)
	}
}
