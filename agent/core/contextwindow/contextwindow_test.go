package contextwindow

import "testing"

func TestHello(t *testing.T) {
	got := Hello()
	want := "hello from contextwindow"
	if got != want {
		t.Errorf("Hello() = %q, want %q", got, want)
	}
}
