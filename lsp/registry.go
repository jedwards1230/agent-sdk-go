package lsp

import (
	"errors"
	"fmt"
	"os/exec"
	"sort"
)

// ErrNotRegistered is wrapped by [Registry.Resolve]'s error when no [Server]
// is registered for the requested language. Distinguish it from
// [ErrNotOnPath] with [errors.Is].
var ErrNotRegistered = errors.New("lsp: no server registered for language")

// ErrNotOnPath is wrapped by [Registry.Resolve]'s error when a registered
// Server's Command cannot be found on PATH. Distinguish it from
// [ErrNotRegistered] with [errors.Is].
var ErrNotOnPath = errors.New("lsp: server command not found on PATH")

// Server is a language server's launch spec: the command looked up on PATH
// plus the project root markers that identify its workspace.
type Server struct {
	// Language is the canonical LSP languageId, e.g. "go", "typescript",
	// "python", "rust".
	Language string
	// Command is the executable looked up on PATH, e.g. "gopls".
	Command string
	// Args are passed to Command unmodified, e.g. "--stdio".
	Args []string
	// RootMarkers are filenames that mark a project root, e.g. "go.mod".
	RootMarkers []string
}

// Resolved is a Server whose Command has been located on PATH.
type Resolved struct {
	Server
	// Path is Command's absolute location on PATH.
	Path string
}

// Registry maps languages to [Server]s and resolves their command on PATH.
// The zero value is not usable — construct with [NewRegistry] or
// [DefaultRegistry].
type Registry struct {
	servers map[string]Server
	// lookPath resolves a command name to its absolute path. It defaults to
	// exec.LookPath; tests inject a fake so Resolve/Available never touch the
	// real PATH.
	lookPath func(string) (string, error)
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		servers:  make(map[string]Server),
		lookPath: exec.LookPath,
	}
}

// defaultServers is the small, hand-curated seed table for [DefaultRegistry]
// — common servers only, not the ~370-server nvim-lspconfig dataset (a later
// consuming layer; see the package doc).
var defaultServers = []Server{
	{Language: "go", Command: "gopls", RootMarkers: []string{"go.mod"}},
	{
		Language:    "typescript",
		Command:     "typescript-language-server",
		Args:        []string{"--stdio"},
		RootMarkers: []string{"package.json", "tsconfig.json"},
	},
	{
		Language:    "javascript",
		Command:     "typescript-language-server",
		Args:        []string{"--stdio"},
		RootMarkers: []string{"package.json", "tsconfig.json"},
	},
	{
		Language:    "python",
		Command:     "pyright-langserver",
		Args:        []string{"--stdio"},
		RootMarkers: []string{"pyproject.toml", "setup.py"},
	},
	{Language: "rust", Command: "rust-analyzer", RootMarkers: []string{"Cargo.toml"}},
	{Language: "c", Command: "clangd", RootMarkers: []string{"compile_commands.json"}},
	{Language: "cpp", Command: "clangd", RootMarkers: []string{"compile_commands.json"}},
}

// DefaultRegistry returns a Registry seeded with [defaultServers].
func DefaultRegistry() *Registry {
	r := NewRegistry()
	for _, s := range defaultServers {
		r.Register(s)
	}
	return r
}

// Register adds s, overriding any existing Server registered for s.Language.
func (r *Registry) Register(s Server) {
	r.servers[s.Language] = s
}

// Lookup returns the Server registered for language, table-only — it never
// touches PATH. Use [Registry.Resolve] to also check installation.
func (r *Registry) Lookup(language string) (Server, bool) {
	s, ok := r.servers[language]
	return s, ok
}

// Resolve looks up language in the table and locates its Command on PATH. The
// returned error wraps [ErrNotRegistered] when no Server is registered for
// language, or [ErrNotOnPath] (itself wrapping the underlying lookPath error,
// typically [exec.ErrNotFound]) when the registered Command is not installed.
func (r *Registry) Resolve(language string) (Resolved, error) {
	s, ok := r.Lookup(language)
	if !ok {
		return Resolved{}, fmt.Errorf("%w: %s", ErrNotRegistered, language)
	}
	path, err := r.lookPath(s.Command)
	if err != nil {
		return Resolved{}, fmt.Errorf("%w: %s: %w", ErrNotOnPath, s.Command, err)
	}
	return Resolved{Server: s, Path: path}, nil
}

// Available returns every registered Server whose Command is found on PATH,
// sorted by Language.
func (r *Registry) Available() []Resolved {
	out := make([]Resolved, 0, len(r.servers))
	for _, s := range r.servers {
		path, err := r.lookPath(s.Command)
		if err != nil {
			continue
		}
		out = append(out, Resolved{Server: s, Path: path})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Language < out[j].Language })
	return out
}
