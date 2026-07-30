// Package skill loads SKILL.md files — the neutral, cross-tool "Agent
// Skills" standard (alongside AGENTS.md, the only other neutral standard the
// SDK builds to) — with progressive disclosure: only a skill's name and
// description enter the model's context up front; the body is read from
// disk only when a skill is actually invoked.
//
// # Why progressive disclosure
//
// A dozen skills whose full bodies rode in every request would be exactly
// the context bloat the SDK's tool/MCP index-first discipline exists to
// avoid (see docs/DESIGN.md, "Context transparency"). [Load] never reads a
// skill's body into the returned [Set]; [Set.Index] returns only [Meta]
// values (name + a byte-budgeted description), and [Set.Body] is the one
// place a body is read — from disk, at invocation time.
//
// # Discovery layout
//
// [Load] takes a caller-supplied list of directories and does not read
// config or decide where skills live — that is the embedder's concern (in
// the reference application, a config.Skills section). Each directory is
// scanned one level deep for the standard layout:
//
//	<dir>/<skill-name>/SKILL.md
//
// A subdirectory without a SKILL.md is not a skill and is silently skipped;
// dot-prefixed entries (editor/OS droppings) are skipped the same way. Every
// other reason a candidate did not become a skill — missing/malformed
// frontmatter, a missing name or description, an oversized body, a
// duplicate name, a symlink the loader refused to follow — is reported as a
// [Diagnostic] rather than silently dropped or, worse, silently truncated:
// half a skill is not a skill.
//
// # Precedence
//
// dirs are scanned in the order given, and the FIRST directory to define a
// name wins; a later directory's definition of the same name is skipped
// with a Diagnostic. This is PATH-style resolution: the SDK does not know
// which directory is "project" and which is "user global", so it makes the
// caller's order the only precedence signal — list the directory that
// should win first.
//
// # Invocation surface
//
// This package does not decide how a skill reaches the model. [Set.Body]
// is the primitive; an embedder can inject the index into a system prompt,
// expose a slash command, or use the optional [NewTool] to expose a single
// dispatcher tool.Tool. NewTool is never registered implicitly — a caller
// that wants it must construct and register it itself.
package skill

import (
	"errors"
	"fmt"
	"os"
	"sort"
)

// DefaultMaxBodyBytes caps a SKILL.md file (frontmatter + body) before
// [Load] skips it with a [Diagnostic] instead of truncating it. 64KiB is
// generous for the "quick to read, links out for detail" body the SKILL.md
// convention asks authors to write; a file past that size is a sign the
// author should move reference material into a separate file the body
// points at, not a reason to hand the model half an instruction set.
const DefaultMaxBodyBytes = 64 * 1024

// DefaultDescriptionBudget caps how many description bytes enter a [Meta] in
// the discovery index before [Load] truncates at a word boundary. 200 bytes
// holds a compact one-to-two sentence summary — enough for the model to
// decide whether to invoke the skill, small enough that a directory of
// dozens of skills stays a cheap index rather than a second prompt.
const DefaultDescriptionBudget = 200

// skillFile is the exact filename the standard requires; case-sensitive,
// matching the file a human or another tool would create by convention.
const skillFile = "SKILL.md"

// ErrNotFound is returned by [Set.Body] when name is not in the Set's index.
var ErrNotFound = errors.New("skill: not found")

// Meta is everything about a skill that enters the discovery index — and by
// design, only this: no body text. It is what a model sees before deciding
// whether to invoke the skill.
type Meta struct {
	// Name identifies the skill, from the SKILL.md frontmatter. Validated at
	// load time to contain no path separators or ".." segment (see the
	// package doc's path-safety note), so it is always safe to use as a map
	// key or, for a caller that chooses to, a path component under the
	// skill's own directory.
	Name string
	// Description is the frontmatter description, truncated to the [Options]
	// DescriptionBudget (or [DefaultDescriptionBudget]) at a word boundary
	// where possible. See Truncated.
	Description string
	// Truncated reports whether Description was cut to fit the budget. A
	// caller that wants the untruncated text can read the skill's SKILL.md
	// directly; the index intentionally does not carry it.
	Truncated bool
}

// Diagnostic is one candidate [Load] could not turn into a skill, or one
// duplicate it dropped. It is a value, not a fatal error: a single bad
// SKILL.md must not cost every other skill in the directory.
type Diagnostic struct {
	// Path is the file or directory the diagnostic is about.
	Path string
	// Err is why it did not become (or stay) a skill.
	Err error
}

func (d Diagnostic) Error() string { return d.Path + ": " + d.Err.Error() }

func (d Diagnostic) Unwrap() error { return d.Err }

// Options tunes a [Load]. The zero value is valid — every field falls back
// to its documented default.
type Options struct {
	// MaxBodyBytes caps a SKILL.md file's size; a file over the cap is
	// skipped with a Diagnostic rather than truncated. <= 0 means
	// [DefaultMaxBodyBytes].
	MaxBodyBytes int
	// DescriptionBudget caps a skill's description in the discovery index,
	// in bytes. <= 0 means [DefaultDescriptionBudget].
	DescriptionBudget int
}

// withDefaults returns o with every unset (<= 0) field replaced by its
// documented default.
func (o Options) withDefaults() Options {
	if o.MaxBodyBytes <= 0 {
		o.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if o.DescriptionBudget <= 0 {
		o.DescriptionBudget = DefaultDescriptionBudget
	}
	return o
}

// entry is a Set's internal record: the public Meta plus enough to find the
// body again later. Deliberately no body field — see the package doc.
type entry struct {
	meta Meta
	path string // the skill's SKILL.md, resolved once at Load time
}

// Set is the immutable result of a [Load]: an in-memory index of the skills
// discovered across the caller's directories. There is no mutator — build a
// new Set (call Load again) to pick up on-disk changes. A Set is safe for
// concurrent use by multiple goroutines because nothing in it is mutated
// after Load returns.
type Set struct {
	skills       map[string]entry
	maxBodyBytes int // reapplied by Body as a defensive re-check, see Body
}

// Load discovers skills across dirs (see the package doc for the layout and
// precedence rule) and returns the resulting Set plus every [Diagnostic]
// encountered. A directory that does not exist is not a Diagnostic — an
// unconfigured skills location is the normal case, not a failure.
func Load(dirs []string, opts Options) (*Set, []Diagnostic) {
	opts = opts.withDefaults()
	set := &Set{skills: make(map[string]entry), maxBodyBytes: opts.MaxBodyBytes}
	var diags []Diagnostic
	for _, dir := range dirs {
		found, dd := loadDir(dir, opts)
		diags = append(diags, dd...)
		for _, e := range found {
			if _, exists := set.skills[e.meta.Name]; exists {
				diags = append(diags, Diagnostic{
					Path: e.path,
					Err:  fmt.Errorf("skill: duplicate name %q; the earlier directory's definition wins", e.meta.Name),
				})
				continue
			}
			set.skills[e.meta.Name] = e
		}
	}
	return set, diags
}

// Index returns the discovery index — every loaded skill's [Meta], sorted by
// Name. No body text is present in the return value; that is the whole
// point of progressive disclosure (see the package doc).
func (s *Set) Index() []Meta {
	out := make([]Meta, 0, len(s.skills))
	for _, e := range s.skills {
		out = append(out, e.meta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Len returns the number of loaded skills.
func (s *Set) Len() int { return len(s.skills) }

// Body reads name's SKILL.md body from disk now — this is the one place in
// the package a body is loaded, deliberately deferred to invocation time
// rather than cached from Load. It re-applies the same symlink refusal and
// size cap Load used, since the file on disk can change between Load and an
// invocation.
func (s *Set) Body(name string) (string, error) {
	e, ok := s.skills[name]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	info, err := os.Lstat(e.path)
	if err != nil {
		return "", fmt.Errorf("skill: reading %s: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("skill: %s: SKILL.md is a symlink, which is not followed", name)
	}
	data, err := readCapped(e.path, s.maxBodyBytes)
	if err != nil {
		return "", fmt.Errorf("skill: reading %s: %w", name, err)
	}
	_, body, err := parseFrontmatter(data)
	if err != nil {
		return "", fmt.Errorf("skill: reparsing %s: %w", name, err)
	}
	return body, nil
}
