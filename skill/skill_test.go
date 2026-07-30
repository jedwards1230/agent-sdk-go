package skill

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeSkill creates <dir>/<name>/SKILL.md with raw content and returns the
// skill's directory.
func writeSkill(t *testing.T, dir, name, content string) string {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return skillDir
}

// skillMD composes a well-formed SKILL.md body.
func skillMD(name, desc, body string) string {
	return "---\nname: " + name + "\ndescription: " + desc + "\n---\n" + body
}

func TestLoadWellFormedSkill(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "review", skillMD("review", "Reviews a diff for correctness.", "# Review\n\nDo the review."))

	set, diags := Load([]string{dir}, Options{})
	if len(diags) != 0 {
		t.Fatalf("diags = %v, want none", diags)
	}
	idx := set.Index()
	if len(idx) != 1 {
		t.Fatalf("Index() len = %d, want 1", len(idx))
	}
	if idx[0].Name != "review" || idx[0].Description != "Reviews a diff for correctness." {
		t.Fatalf("Index()[0] = %+v", idx[0])
	}
	body, err := set.Body("review")
	if err != nil {
		t.Fatalf("Body: %v", err)
	}
	if body != "# Review\n\nDo the review." {
		t.Fatalf("Body() = %q", body)
	}
}

func TestLoadMissingFrontmatter(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "broken", "# Just a heading\n\nno frontmatter at all")

	set, diags := Load([]string{dir}, Options{})
	if set.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", set.Len())
	}
	if len(diags) != 1 {
		t.Fatalf("diags = %v, want 1", diags)
	}
}

func TestLoadMissingName(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "broken", "---\ndescription: has no name\n---\nbody")

	set, diags := Load([]string{dir}, Options{})
	if set.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", set.Len())
	}
	if len(diags) != 1 || !strings.Contains(diags[0].Err.Error(), "name") {
		t.Fatalf("diags = %v, want one mentioning name", diags)
	}
}

func TestLoadMissingDescription(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "broken", "---\nname: broken\n---\nbody")

	set, diags := Load([]string{dir}, Options{})
	if set.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", set.Len())
	}
	if len(diags) != 1 || !strings.Contains(diags[0].Err.Error(), "description") {
		t.Fatalf("diags = %v, want one mentioning description", diags)
	}
}

func TestLoadMalformedFrontmatter(t *testing.T) {
	dir := t.TempDir()
	// Unclosed fence.
	writeSkill(t, dir, "unclosed", "---\nname: unclosed\ndescription: never closes\nbody without a closing fence")
	// Invalid YAML inside the fence.
	writeSkill(t, dir, "badyaml", "---\nname: [this is not, valid: yaml\n---\nbody")

	set, diags := Load([]string{dir}, Options{})
	if set.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", set.Len())
	}
	if len(diags) != 2 {
		t.Fatalf("diags = %v, want 2", diags)
	}
}

func TestLoadDescriptionTruncation(t *testing.T) {
	desc := "This description is long enough that it must be truncated at the configured budget for the discovery index to stay small and auditable across many skills."
	dir := t.TempDir()
	writeSkill(t, dir, "long", skillMD("long", desc, "body"))

	set, diags := Load([]string{dir}, Options{DescriptionBudget: 40})
	if len(diags) != 0 {
		t.Fatalf("diags = %v, want none", diags)
	}
	idx := set.Index()
	if len(idx) != 1 {
		t.Fatalf("Index() len = %d, want 1", len(idx))
	}
	got := idx[0]
	if !got.Truncated {
		t.Fatalf("Truncated = false, want true")
	}
	if len(got.Description) > 40 {
		t.Fatalf("Description len = %d, want <= 40 (%q)", len(got.Description), got.Description)
	}
	if !strings.HasSuffix(got.Description, truncMarker) {
		t.Fatalf("Description = %q, want suffix %q", got.Description, truncMarker)
	}
	// Word-boundary: the character right before the marker must not split a
	// word the source description continues past the cut.
	before := strings.TrimSuffix(got.Description, truncMarker)
	if !strings.HasPrefix(desc, before) {
		t.Fatalf("truncated prefix %q is not a prefix of the source description", before)
	}
	if idx := strings.IndexByte(desc, ' '); idx >= 0 && len(before) < len(desc) {
		next := desc[len(before):]
		if len(next) > 0 && next[0] != ' ' {
			t.Fatalf("cut %q does not land on a word boundary; next byte is %q", before, next[:1])
		}
	}
}

func TestLoadDescriptionUnderBudgetIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "short", skillMD("short", "Short description.", "body"))

	set, _ := Load([]string{dir}, Options{DescriptionBudget: 200})
	got := set.Index()[0]
	if got.Truncated {
		t.Fatalf("Truncated = true, want false")
	}
	if got.Description != "Short description." {
		t.Fatalf("Description = %q", got.Description)
	}
}

func TestLoadOversizedBodySkippedNotTruncated(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", 100)
	writeSkill(t, dir, "huge", skillMD("huge", "A skill with a huge body.", big))

	set, diags := Load([]string{dir}, Options{MaxBodyBytes: 50})
	if set.Len() != 0 {
		t.Fatalf("Len() = %d, want 0 (oversized skill must be skipped, not indexed truncated)", set.Len())
	}
	if len(diags) != 1 || !strings.Contains(diags[0].Err.Error(), "cap") {
		t.Fatalf("diags = %v, want one mentioning the cap", diags)
	}
	if _, err := set.Body("huge"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Body(huge) err = %v, want ErrNotFound", err)
	}
}

func TestLoadDuplicateNameFirstDirWins(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	writeSkill(t, dirA, "shared", skillMD("shared", "from A", "body A"))
	writeSkill(t, dirB, "shared", skillMD("shared", "from B", "body B"))

	set, diags := Load([]string{dirA, dirB}, Options{})
	if set.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", set.Len())
	}
	if len(diags) != 1 || !strings.Contains(diags[0].Err.Error(), "duplicate") {
		t.Fatalf("diags = %v, want one mentioning duplicate", diags)
	}
	got := set.Index()[0]
	if got.Description != "from A" {
		t.Fatalf("Description = %q, want the FIRST directory's definition (from A)", got.Description)
	}
	body, err := set.Body("shared")
	if err != nil {
		t.Fatalf("Body: %v", err)
	}
	if body != "body A" {
		t.Fatalf("Body() = %q, want the first directory's body", body)
	}

	// Reversing the directory order reverses the winner: precedence follows
	// the caller's list order, not any property of the skill itself.
	set2, _ := Load([]string{dirB, dirA}, Options{})
	got2 := set2.Index()[0]
	if got2.Description != "from B" {
		t.Fatalf("reversed order Description = %q, want from B", got2.Description)
	}
}

func TestLoadNonSkillFileIgnored(t *testing.T) {
	dir := t.TempDir()
	// A stray file directly under dir, not a subdirectory.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("not a skill"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// A subdirectory whose markdown file isn't named SKILL.md.
	otherDir := filepath.Join(dir, "notaskill")
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(otherDir, "other.md"), []byte(skillMD("x", "y", "z")), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	set, diags := Load([]string{dir}, Options{})
	if set.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", set.Len())
	}
	if len(diags) != 0 {
		t.Fatalf("diags = %v, want none (a non-SKILL.md file is silently ignored)", diags)
	}
}

func TestLoadEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	set, diags := Load([]string{dir}, Options{})
	if set.Len() != 0 || len(diags) != 0 {
		t.Fatalf("Len() = %d, diags = %v, want 0 and none", set.Len(), diags)
	}
}

func TestLoadNonexistentDirectoryIsNotFatal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	set, diags := Load([]string{dir}, Options{})
	if set.Len() != 0 || len(diags) != 0 {
		t.Fatalf("Len() = %d, diags = %v, want 0 and none", set.Len(), diags)
	}
}

// TestIndexNeverCarriesBody proves the progressive-disclosure property
// structurally: Meta has no field that could hold body text, so nothing in
// the compiled index type can leak one regardless of what Load does
// internally.
func TestIndexNeverCarriesBody(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "s", skillMD("s", "d", "the body text that must never appear in the index"))
	set, _ := Load([]string{dir}, Options{})
	m := set.Index()[0]
	// Meta{Name, Description, Truncated} — there is no way to reach body
	// text through this value. This assertion documents the invariant a
	// future field addition to Meta must preserve.
	_ = struct {
		Name        string
		Description string
		Truncated   bool
	}(m)
}

// TestBodyReadsFromDiskAtInvocationNotAtLoad proves Load did not cache the
// body in memory: deleting the SKILL.md after Load succeeds but before
// Body is called makes Body fail, which is only possible if Body performs
// real disk IO at invocation time rather than returning something captured
// during Load.
func TestBodyReadsFromDiskAtInvocationNotAtLoad(t *testing.T) {
	dir := t.TempDir()
	skillDir := writeSkill(t, dir, "gone", skillMD("gone", "will be deleted", "body"))

	set, diags := Load([]string{dir}, Options{})
	if len(diags) != 0 {
		t.Fatalf("diags = %v, want none", diags)
	}
	if set.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", set.Len())
	}

	if err := os.RemoveAll(skillDir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}

	if _, err := set.Body("gone"); err == nil {
		t.Fatal("Body() succeeded after the file was deleted; the body must not have been cached at Load time")
	}
}

func TestLoadSymlinkedSkillDirectoryNotFollowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require elevated privileges on windows")
	}
	dir := t.TempDir()
	outside := t.TempDir()
	writeSkill(t, outside, "secret", skillMD("secret", "outside the skills root", "sensitive body"))

	link := filepath.Join(dir, "secret")
	if err := os.Symlink(filepath.Join(outside, "secret"), link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	set, diags := Load([]string{dir}, Options{})
	if set.Len() != 0 {
		t.Fatalf("Len() = %d, want 0 (a symlinked skill directory must not be followed)", set.Len())
	}
	if len(diags) != 1 || !strings.Contains(diags[0].Err.Error(), "symlink") {
		t.Fatalf("diags = %v, want one mentioning symlink", diags)
	}
}

func TestLoadSymlinkedSkillFileNotFollowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require elevated privileges on windows")
	}
	dir := t.TempDir()
	secret := filepath.Join(t.TempDir(), "passwd")
	if err := os.WriteFile(secret, []byte("sensitive"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	skillDir := filepath.Join(dir, "evil")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	link := filepath.Join(skillDir, "SKILL.md")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	set, diags := Load([]string{dir}, Options{})
	if set.Len() != 0 {
		t.Fatalf("Len() = %d, want 0 (a symlinked SKILL.md must not be followed)", set.Len())
	}
	if len(diags) != 1 || !strings.Contains(diags[0].Err.Error(), "symlink") {
		t.Fatalf("diags = %v, want one mentioning symlink", diags)
	}
}

func TestLoadNameEscapeRejected(t *testing.T) {
	tests := []struct {
		name    string
		fmName  string
		wantErr string
	}{
		{"dot-dot-slash", "../../etc/passwd", "path separator"},
		{"absolute-looking", "/etc/passwd", "path separator"},
		{"bare-dotdot", "..", "relative path segment"},
		{"backslash", `evil\name`, "path separator"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeSkill(t, dir, "candidate", skillMD(tc.fmName, "escapes its directory", "body"))

			set, diags := Load([]string{dir}, Options{})
			if set.Len() != 0 {
				t.Fatalf("Len() = %d, want 0", set.Len())
			}
			if len(diags) != 1 || !strings.Contains(diags[0].Err.Error(), tc.wantErr) {
				t.Fatalf("diags = %v, want one mentioning %q", diags, tc.wantErr)
			}
		})
	}
}
