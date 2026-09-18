package materialize

import "testing"

func TestHello(t *testing.T) {
	got := Hello()
	want := "hello from materialize"
	if got != want {
		t.Errorf("Hello() = %q, want %q", got, want)
	}
}
