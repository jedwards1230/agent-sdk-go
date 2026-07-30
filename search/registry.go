package search

import (
	"fmt"
	"slices"
	"sync"
)

// Factory constructs a [Provider] from a [Config]. An implementation
// registers its factory under a unique name via [Register]; [Build]
// dispatches to it by name.
type Factory func(cfg Config) (Provider, error)

var (
	mu       sync.RWMutex
	registry = map[string]Factory{}
)

// Register adds a named [Factory] to the package registry, so [Build] can
// construct that provider by name without the caller importing the
// implementation package directly — a blank import for its registration side
// effect is enough:
//
//	import _ "github.com/jedwards1230/agent-sdk-go/search/brave"
//
// Implementations call Register from their own package, typically an
// init(). This is the whole extension point: adding a third backend means
// writing one package that implements [Provider] and registers itself here
// — nothing in package search changes.
//
// Registering the same name twice panics — a programming error caught at
// init time, the same contract as [database/sql.Register].
func Register(name string, factory Factory) {
	mu.Lock()
	defer mu.Unlock()
	if name == "" {
		panic("search: Register called with empty name")
	}
	if factory == nil {
		panic("search: Register called with nil factory for " + name)
	}
	if _, dup := registry[name]; dup {
		panic("search: Register called twice for provider " + name)
	}
	registry[name] = factory
}

// Build constructs the provider registered under name, resolving cfg's
// credentials and endpoint against it. It errors if no provider is
// registered under that name (typically a missing blank import) or if the
// provider's own constructor rejects cfg (e.g. a required field left empty).
func Build(name string, cfg Config) (Provider, error) {
	mu.RLock()
	factory, ok := registry[name]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("search: no provider registered for %q (forgot a blank import of its package?); registered: %v", name, Registered())
	}
	return factory(cfg)
}

// Registered returns the names of every currently registered provider,
// sorted, for diagnostics — e.g. an embedder's config validation error
// listing valid choices.
func Registered() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
