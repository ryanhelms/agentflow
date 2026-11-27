package acpregistry

import "path/filepath"

// isMusl reports whether this Linux system's C library is musl: it has a
// musl dynamic loader.
func isMusl() bool {
	m, _ := filepath.Glob("/lib/ld-musl-*.so.1")
	return len(m) > 0
}
