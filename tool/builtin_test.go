package tool

import "testing"

func TestBuiltinsReturnsSevenTools(t *testing.T) {
	dir := t.TempDir()
	tools := Builtins(dir)
	if len(tools) != 7 {
		t.Fatalf("len(Builtins) = %d, want 7", len(tools))
	}
	want := map[string]bool{
		"bash": true, "read": true, "edit": true, "write": true,
		"grep": true, "glob": true, "ls": true,
	}
	for _, tl := range tools {
		if !want[tl.Name()] {
			t.Fatalf("unexpected tool name %q", tl.Name())
		}
		delete(want, tl.Name())
	}
	if len(want) != 0 {
		t.Fatalf("missing tools: %v", want)
	}
}

func TestRegisterBuiltins(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry()
	if err := RegisterBuiltins(r, dir); err != nil {
		t.Fatalf("RegisterBuiltins: %v", err)
	}
	if r.Len() != 7 {
		t.Fatalf("registry Len = %d, want 7", r.Len())
	}
	for _, name := range []string{"bash", "read", "edit", "write", "grep", "glob", "ls"} {
		if _, ok := r.Get(name); !ok {
			t.Fatalf("registry missing tool %q", name)
		}
	}
}

func TestRegisterBuiltinsDuplicateError(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(NewBash(dir))
	err := RegisterBuiltins(r, dir)
	if err == nil {
		t.Fatal("RegisterBuiltins err = nil, want ErrDuplicate for pre-registered bash")
	}
}
