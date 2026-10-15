package mesh_test

import (
	"encoding/json"
	"testing"

	mesh "github.com/hollis-labs/substrate/mesh"
)

func TestDecodedDiagnosticWithoutCause(t *testing.T) {
	var diagnostic mesh.DispatchError
	if err := json.Unmarshal([]byte(`{"cause":null,"diagnostic":"provider_unavailable"}`), &diagnostic); err != nil {
		t.Fatal(err)
	}
	if diagnostic.Error() != string(mesh.DiagnosticProviderUnavailable) || diagnostic.Unwrap() != nil {
		t.Fatal("decoded diagnostic without cause did not remain usable")
	}
}
