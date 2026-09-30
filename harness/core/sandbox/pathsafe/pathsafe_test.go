package pathsafe

import "testing"

func TestHello(t *testing.T) {
	got := Hello()
	want := "hello from pathsafe"
	if got != want {
		t.Errorf("Hello() = %q, want %q", got, want)
	}
}
