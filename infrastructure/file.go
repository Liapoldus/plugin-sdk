package infrastructure

import (
	"os"
	"path/filepath"
)

// Material paths are operator-selected, not request input. Root the read in
// that directory so a basename or symlink cannot escape it during the read.
func readMaterialFile(path string) (contents []byte, failure error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := root.Close(); err != nil && failure == nil {
			contents, failure = nil, err
		}
	}()
	return root.ReadFile(filepath.Base(path))
}
