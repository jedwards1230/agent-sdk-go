package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// stubTool is a minimal [Tool] implementation for registry tests.
type stubTool struct {
	name string
}

func (s stubTool) Name() string        { return s.name }
func (s stubTool) Description() string { return "stub: " + s.name }
func (s stubTool) Spec() Schema        { return ObjectSchema(nil, nil) }
func (s stubTool) Run(context.Context, json.RawMessage) (Result, error) {
	return Result{Content: s.name}, nil
}

func TestNewRegistry(t *testing.T) {
	r := NewRegistry(stubTool{name: "a"}, stubTool{name: "b"})
	if r.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", r.Len())
	}
}

func TestNewRegistryPanicsOnDuplicate(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate name")
		}
	}()
	NewRegistry(stubTool{name: "a"}, stubTool{name: "a"})
}

func TestNewRegistryPanicsOnEmptyName(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on empty name")
		}
	}()
	NewRegistry(stubTool{name: ""})
}

func TestRegisterDuplicate(t *testing.T) {
	r := NewRegistry(stubTool{name: "a"})
	err := r.Register(stubTool{name: "a"})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("Register duplicate: err = %v, want ErrDuplicate", err)
	}
}

func TestRegisterEmptyName(t *testing.T) {
	r := NewRegistry()
	err := r.Register(stubTool{name: ""})
	if !errors.Is(err, ErrEmptyName) {
		t.Fatalf("Register empty: err = %v, want ErrEmptyName", err)
	}
}

func TestGet(t *testing.T) {
	r := NewRegistry(stubTool{name: "a"})
	tl, ok := r.Get("a")
	if !ok || tl.Name() != "a" {
		t.Fatalf("Get(a) = %v, %v", tl, ok)
	}
	if _, ok := r.Get("missing"); ok {
		t.Fatalf("Get(missing) = true, want false")
	}
}

func TestListSorted(t *testing.T) {
	r := NewRegistry(stubTool{name: "zeta"}, stubTool{name: "alpha"}, stubTool{name: "mid"})
	got := r.List()
	want := []string{"alpha", "mid", "zeta"}
	for i, w := range want {
		if got[i].Name() != w {
			t.Fatalf("List()[%d] = %q, want %q", i, got[i].Name(), w)
		}
	}
}

func TestNamesSorted(t *testing.T) {
	r := NewRegistry(stubTool{name: "zeta"}, stubTool{name: "alpha"})
	got := r.Names()
	want := []string{"alpha", "zeta"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
}

func TestUnregister(t *testing.T) {
	r := NewRegistry(stubTool{name: "a"})
	r.Unregister("a")
	if r.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", r.Len())
	}
	// unregistering an absent tool is a no-op.
	r.Unregister("nope")
	if r.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", r.Len())
	}
}

func TestCloneIsolation(t *testing.T) {
	orig := NewRegistry(stubTool{name: "a"})
	clone := orig.Clone()

	if err := clone.Register(stubTool{name: "b"}); err != nil {
		t.Fatalf("Register on clone: %v", err)
	}
	if orig.Len() != 1 {
		t.Fatalf("orig.Len() = %d, want 1 (clone mutation leaked)", orig.Len())
	}
	if clone.Len() != 2 {
		t.Fatalf("clone.Len() = %d, want 2", clone.Len())
	}

	clone.Unregister("a")
	if _, ok := orig.Get("a"); !ok {
		t.Fatalf("orig lost tool %q after clone.Unregister", "a")
	}
}

func TestRegistryConcurrency(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		i := i
		wg.Add(3)
		go func() {
			defer wg.Done()
			_ = r.Register(stubTool{name: fmt.Sprintf("t%d", i)})
		}()
		go func() {
			defer wg.Done()
			r.Get(fmt.Sprintf("t%d", i))
		}()
		go func() {
			defer wg.Done()
			r.List()
		}()
	}
	wg.Wait()
}
