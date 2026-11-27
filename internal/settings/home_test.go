package settings

import (
	"errors"
	"os"
	"testing"
)

// A Service without an absolute home refuses instead of reading or writing
// the personal layer relative to the working directory, which may be a
// repository.
func TestServiceWithoutHomeNeverUsesWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("config.yml", []byte("autonomy: {local: auto, remote: auto}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, home := range []string{"", "relative"} {
		s := &Service{Home: home}
		if _, err := s.read(); !errors.Is(err, errNoHome) {
			t.Errorf("Home %q: read() = %v, want errNoHome", home, err)
		}
		if _, err := s.lock(); !errors.Is(err, errNoHome) {
			t.Errorf("Home %q: lock() = %v, want errNoHome", home, err)
		}
		if _, err := s.Journal(t.Context()); !errors.Is(err, errNoHome) {
			t.Errorf("Home %q: Journal() = %v, want errNoHome", home, err)
		}
		if err := s.journalWritable(); !errors.Is(err, errNoHome) {
			t.Errorf("Home %q: journalWritable() = %v, want errNoHome", home, err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("the service wrote to the working directory: %v", entries)
	}
}
