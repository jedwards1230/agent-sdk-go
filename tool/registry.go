package tool

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// ErrDuplicate is returned by [Registry.Register] when a tool name is already
// registered.
var ErrDuplicate = errors.New("tool: name already registered")

// ErrEmptyName is returned by [Registry.Register] when a tool's Name is empty.
var ErrEmptyName = errors.New("tool: empty tool name")

// Registry is a mutable, concurrency-safe set of tools keyed by Name. The
// zero value is not usable — construct with [NewRegistry]. Per-agent
// isolation comes from [Registry.Clone].
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry returns a Registry containing tools. It panics if two tools
// share a name or a name is empty — a construction-time programming error.
func NewRegistry(tools ...Tool) *Registry {
	r := &Registry{tools: make(map[string]Tool, len(tools))}
	for _, t := range tools {
		if err := r.Register(t); err != nil {
			panic(err)
		}
	}
	return r
}

// Register adds t. It returns an error if t's name is empty or already
// registered.
func (r *Registry) Register(t Tool) error {
	name := t.Name()
	if name == "" {
		return ErrEmptyName
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicate, name)
	}
	r.tools[name] = t
	return nil
}

// Unregister removes the tool with name, if present. It is a no-op
// otherwise.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
}

// Get returns the tool registered under name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List returns all registered tools sorted by name — a deterministic order
// for building provider tool specs.
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Names returns all registered tool names, sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.tools))
	for name := range r.tools {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Len returns the number of registered tools.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}

// Clone returns an independent Registry with the same tool membership. The
// Tool values are shared (they are stateless configuration); registering or
// unregistering on the clone does not affect the original. Use it to give
// each agent or session its own tool set.
func (r *Registry) Clone() *Registry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := &Registry{tools: make(map[string]Tool, len(r.tools))}
	for name, t := range r.tools {
		out.tools[name] = t
	}
	return out
}
