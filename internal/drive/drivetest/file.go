package drivetest

import (
	"os"
	"path/filepath"
)

// writeFile writes a file readable by its owner only, creating its directory.
func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o600)
}
