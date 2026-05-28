package groundtruth

import (
	"io/fs"
	"path/filepath"
	"strings"

	ignore "github.com/sabhiram/go-gitignore"
)

// ScanCap is the maximum number of .md files walkMDFiles collects
// before stopping. Exported so CLI commands can surface the same cap
// in their --help text without hard-coding the number.
const ScanCap = 500

// SkipDirs is the set of directory names walkMDFiles prunes
// unconditionally. Matches the same list the session-start scanner
// and the facts-impact scanner use, so both surfaces stay consistent.
var SkipDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"dist":         true,
	"build":        true,
	"target":       true,
	".next":        true,
	"out":          true,
}

// walkMDFiles returns up to cap .md files under root, skipping SkipDirs,
// hidden directories, and any path matched by ig (when non-nil). capped is
// true when the walk was stopped early because cap was reached.
func walkMDFiles(root string, cap int, ig *ignore.GitIgnore) (targets []string, capped bool, err error) {
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkerr error) error {
		if walkerr != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			name := d.Name()
			if SkipDirs[name] || (strings.HasPrefix(name, ".") && name != ".") {
				return filepath.SkipDir
			}
			if ig != nil && ig.MatchesPath(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if ig != nil && ig.MatchesPath(rel) {
			return nil
		}
		if strings.ToLower(filepath.Ext(path)) == ".md" {
			targets = append(targets, path)
			if len(targets) >= cap {
				capped = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	return targets, capped, err
}
