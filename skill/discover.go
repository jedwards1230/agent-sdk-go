package skill

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// fmDelim is the frontmatter fence line, opening and closing.
const fmDelim = "---"

// frontmatter is the recognized SKILL.md header. It is real YAML (the SDK
// already depends on gopkg.in/yaml.v3 for compose manifests) rather than a
// bespoke reader, so the standard's other optional keys (license,
// allowed-tools, metadata, ...) parse without error — this struct simply
// does not look at them yet. yaml.v3 ignores fields it can't map by default,
// so a future key here is forward-compatible for files written today.
type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// loadDir scans one caller-supplied directory, one level deep, for
// `<dir>/<name>/SKILL.md` skills. A missing directory yields nothing (not a
// Diagnostic); every other failure to read it is one Diagnostic naming the
// directory, and every failure to load one candidate subdirectory is one
// Diagnostic naming that subdirectory's SKILL.md.
func loadDir(dir string, opts Options) ([]entry, []Diagnostic) {
	top, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, []Diagnostic{{Path: dir, Err: err}}
	}

	var (
		found []entry
		diags []Diagnostic
	)
	for _, sub := range top {
		if strings.HasPrefix(sub.Name(), ".") {
			continue // editor/OS droppings, never a skill
		}
		if sub.Type()&fs.ModeSymlink != 0 {
			// A symlinked "skill directory" could point anywhere on disk;
			// following it would let an untrusted skills directory read
			// outside itself. Refused, not followed — reported so the
			// caller can see why a skill they expected is missing.
			diags = append(diags, Diagnostic{
				Path: filepath.Join(dir, sub.Name()),
				Err:  errors.New("skill: symlinked skill directory is not followed"),
			})
			continue
		}
		if !sub.IsDir() {
			continue // a stray file directly under dir (e.g. a README) is not a skill
		}
		e, diag := loadSkillDir(filepath.Join(dir, sub.Name()), opts)
		if diag != nil {
			diags = append(diags, *diag)
		}
		if e != nil {
			found = append(found, *e)
		}
	}
	return found, diags
}

// loadSkillDir loads one `<dir>/<name>/SKILL.md` candidate. A directory with
// no SKILL.md is not a skill and not a Diagnostic — only a directory that
// looks like it was meant to be a skill but failed for some reason is
// reported.
func loadSkillDir(skillDir string, opts Options) (*entry, *Diagnostic) {
	mdPath := filepath.Join(skillDir, skillFile)
	info, err := os.Lstat(mdPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, &Diagnostic{Path: mdPath, Err: err}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		// Same reasoning as the directory case: a symlinked SKILL.md could
		// resolve to any file on disk (e.g. outside skillDir entirely).
		return nil, &Diagnostic{Path: mdPath, Err: errors.New("skill: SKILL.md is a symlink, which is not followed")}
	}

	data, err := readCapped(mdPath, opts.MaxBodyBytes)
	if err != nil {
		return nil, &Diagnostic{Path: mdPath, Err: err}
	}
	fm, _, err := parseFrontmatter(data)
	if err != nil {
		return nil, &Diagnostic{Path: mdPath, Err: err}
	}
	if err := validName(fm.Name); err != nil {
		return nil, &Diagnostic{Path: mdPath, Err: err}
	}
	if strings.TrimSpace(fm.Description) == "" {
		return nil, &Diagnostic{Path: mdPath, Err: errors.New("skill: frontmatter is missing description")}
	}

	desc, truncated := truncateDescription(fm.Description, opts.DescriptionBudget)
	return &entry{
		meta: Meta{Name: fm.Name, Description: desc, Truncated: truncated},
		path: mdPath,
	}, nil
}

// parseFrontmatter splits src into its frontmatter and the body below the
// closing fence. Unlike gofer's internal/usercmd command loader, a SKILL.md
// with missing or malformed frontmatter does NOT degrade to "no
// frontmatter, body only" — the standard requires name and description, and
// a skill silently missing both would be indexed as an empty, unselectable
// entry. It is a load failure here, reported as a Diagnostic by the caller.
func parseFrontmatter(src []byte) (frontmatter, string, error) {
	// A UTF-8 BOM ahead of the fence is common from editors on Windows and
	// would otherwise make the first line BOM+"---" — not a fence.
	s := strings.TrimPrefix(string(src), "\ufeff")

	first, rest, found := strings.Cut(s, "\n")
	if fenceLine(first) != fmDelim {
		return frontmatter{}, "", errors.New("skill: missing opening --- frontmatter fence")
	}
	if !found {
		return frontmatter{}, "", errors.New("skill: frontmatter opens with --- but the file has no further lines")
	}

	lines := strings.Split(rest, "\n")
	for i, ln := range lines {
		if fenceLine(ln) != fmDelim {
			continue
		}
		var fm frontmatter
		header := strings.Join(lines[:i], "\n")
		if err := yaml.Unmarshal([]byte(header), &fm); err != nil {
			return frontmatter{}, "", fmt.Errorf("skill: parsing frontmatter: %w", err)
		}
		return fm, strings.Join(lines[i+1:], "\n"), nil
	}
	return frontmatter{}, "", errors.New("skill: frontmatter opened with --- but was never closed")
}

// fenceLine normalizes one line for the fence comparison: CRLF's carriage
// return and trailing horizontal whitespace are not meaningful in a fence.
func fenceLine(s string) string { return strings.TrimRight(s, " \t\r") }

// validName reports whether name is safe to use as a Set key and, since it
// is fully attacker-controlled frontmatter content, as a path component. The
// SDK does not itself join name onto a directory anywhere today, but a name
// that could escape one is a landmine for any embedder-side code that later
// does (e.g. resolving a resource file alongside SKILL.md by skill name) —
// reject it at the one place it is read, not downstream.
func validName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("skill: frontmatter is missing name")
	}
	if name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("skill: name %q must not contain a path separator or be a relative path segment", name)
	}
	return nil
}

// truncMarker replaces cut text so a truncated description is visibly
// incomplete rather than looking like a short, complete one.
const truncMarker = "…"

// truncateDescription cuts s to at most budget bytes (including the
// trailing [truncMarker], once added), preferring to cut at the last word
// boundary within the budget so a truncated description reads as a
// sentence fragment rather than a chopped word. budget <= 0 or s already
// fitting returns s unchanged with truncated=false.
func truncateDescription(s string, budget int) (out string, truncated bool) {
	if budget <= 0 || len(s) <= budget {
		return s, false
	}
	cut := budget - len(truncMarker)
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if idx := strings.LastIndexByte(s[:cut], ' '); idx > 0 {
		cut = idx
	}
	return strings.TrimRight(s[:cut], " ") + truncMarker, true
}

// readCapped reads path, refusing anything larger than maxBytes (<= 0 = no
// cap). The cap is enforced on the open handle with an [io.LimitReader]
// rather than by stat-then-read, so a file that grows between the two calls
// can't beat the check. It reads one byte past the cap so an at-cap file is
// accepted and an over-cap one is detected without a second syscall to size
// it.
func readCapped(path string, maxBytes int) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // path is built from a directory walk under a caller-supplied root, not user-typed
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // read-only: a Close error says nothing about the bytes already read
	if maxBytes <= 0 {
		return io.ReadAll(f)
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(maxBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBytes {
		return nil, fmt.Errorf("skill: over the %d-byte SKILL.md cap", maxBytes)
	}
	return data, nil
}
