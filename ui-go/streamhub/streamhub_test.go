package streamhub

import "testing"

func TestHello(t *testing.T) {
	got := Hello()
	want := "hello from streamhub"
	if got != want {
		t.Errorf("Hello() = %q, want %q", got, want)
	}
}
