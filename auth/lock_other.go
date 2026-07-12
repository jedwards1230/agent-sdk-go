//go:build !unix

package auth

// lockFile is a no-op on platforms without flock (Windows v1). In-process
// single-flight still applies; cross-process refresh races degrade to
// last-writer-wins, which the atomic replace keeps from corrupting the file.
func (s *Store) lockFile() (func(), error) {
	return func() {}, nil
}
