package tool

// Builtins returns the standard builtin tool set rooted at dir: bash, read,
// edit, write, grep, glob, ls, update_plan. Import the whole set with
// NewRegistry(Builtins(dir)...). update_plan is stateless and ignores dir.
func Builtins(dir string) []Tool {
	return []Tool{
		NewBash(dir),
		NewRead(dir),
		NewEdit(dir),
		NewWrite(dir),
		NewGrep(dir),
		NewGlob(dir),
		NewLS(dir),
		NewUpdatePlan(),
	}
}

// RegisterBuiltins registers each of [Builtins] rooted at dir onto r,
// returning the first registration error encountered.
func RegisterBuiltins(r *Registry, dir string) error {
	for _, t := range Builtins(dir) {
		if err := r.Register(t); err != nil {
			return err
		}
	}
	return nil
}
