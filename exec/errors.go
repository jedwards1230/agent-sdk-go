package exec

// SchemaError reports that a run's final result did not satisfy the output
// schema. Path is a JSON-pointer-ish location of the offending value ("" for the
// root value); Msg describes the violation.
type SchemaError struct {
	Path string
	Msg  string
}

// Error renders the violation, e.g.
// "exec: output schema: /foo: expected string, got number", or
// "exec: output schema: final result is not valid JSON" at the root.
func (e *SchemaError) Error() string {
	if e.Path == "" {
		return "exec: output schema: " + e.Msg
	}
	return "exec: output schema: " + e.Path + ": " + e.Msg
}
