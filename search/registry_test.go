package search_test

import (
	"context"
	"slices"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/search"
)

func TestRegisterAndBuild(t *testing.T) {
	name := "registry-test-fixture"
	search.Register(name, func(cfg search.Config) (search.Provider, error) {
		if cfg.APIKey != "k" {
			t.Fatalf("factory received cfg.APIKey = %q, want %q", cfg.APIKey, "k")
		}
		return fixtureProvider{}, nil
	})

	if !slices.Contains(search.Registered(), name) {
		t.Fatalf("Registered() = %v, want it to contain %q", search.Registered(), name)
	}

	p, err := search.Build(name, search.Config{APIKey: "k"})
	if err != nil {
		t.Fatalf("Build(%q): %v", name, err)
	}
	if p.Name() != "fixture" {
		t.Errorf("Build(%q).Name() = %q, want %q", name, p.Name(), "fixture")
	}
}

func TestBuildUnregisteredName(t *testing.T) {
	_, err := search.Build("no-such-provider-registered-anywhere", search.Config{})
	if err == nil {
		t.Fatal("Build with an unregistered name: want an error, got nil")
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	name := "registry-test-dup"
	search.Register(name, func(search.Config) (search.Provider, error) { return fixtureProvider{}, nil })

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Register with a duplicate name: want a panic, got none")
		}
	}()
	search.Register(name, func(search.Config) (search.Provider, error) { return fixtureProvider{}, nil })
}

func TestRegisterEmptyNamePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Register with an empty name: want a panic, got none")
		}
	}()
	search.Register("", func(search.Config) (search.Provider, error) { return fixtureProvider{}, nil })
}

// fixtureProvider is a minimal, valid search.Provider for registry tests
// that need Build to actually succeed.
type fixtureProvider struct{}

func (fixtureProvider) Name() string { return "fixture" }

func (fixtureProvider) Search(_ context.Context, _ string, _ search.Options) (*search.Results, error) {
	return &search.Results{}, nil
}
