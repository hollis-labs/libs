package permission

import "testing"

func TestHello(t *testing.T) {
	got := Hello()
	want := "hello from permission"
	if got != want {
		t.Errorf("Hello() = %q, want %q", got, want)
	}
}
