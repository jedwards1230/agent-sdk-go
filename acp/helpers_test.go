package acp_test

import (
	"encoding/json"
	"reflect"
	"testing"
)

// assertJSONEqual compares got (raw JSON bytes) against want (a JSON literal)
// after normalizing both through json.Unmarshal into any, so key order and
// whitespace differences don't fail the assertion.
func assertJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()

	var gotVal, wantVal any
	if err := json.Unmarshal(got, &gotVal); err != nil {
		t.Fatalf("unmarshal got JSON %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantVal); err != nil {
		t.Fatalf("unmarshal want JSON %s: %v", want, err)
	}
	if !reflect.DeepEqual(gotVal, wantVal) {
		t.Errorf("JSON mismatch:\n got:  %s\n want: %s", got, want)
	}
}
