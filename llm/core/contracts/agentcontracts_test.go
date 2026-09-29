package agentcontracts

import "testing"

func TestHello(t *testing.T) {
	got := Hello()
	want := "hello from agentcontracts"
	if got != want {
		t.Errorf("Hello() = %q, want %q", got, want)
	}
}
