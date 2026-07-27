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
// hidden directories, any path matched by ig (when non-nil), and any
// directory listed in excludeDirs (absolute paths, compared cleaned).
// capped is true when the walk was stopped early because cap was reached.
//
// excludeDirs exists for the ground-truth truth_dir (incident-1): the
// truth tree is the evidence the detector checks claims AGAINST, so
// scanning it for claims is circular — do-not-claim.md matches its own
// rules by definition, and audit-log.md is a machine-appended log that
// grows without bound (834 KB in the job-hunt dogfood project, which
// turned the session-start fuzzy scan into an hours-long CPU burn).
func walkMDFiles(root string, cap int, ig *ignore.GitIgnore, excludeDirs ...string) (targets []string, capped bool, err error) {
	excluded := make(map[string]bool, len(excludeDirs))
	for _, d := range excludeDirs {
		if d != "" {
			excluded[filepath.Clean(d)] = true
		}
	}
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
			if excluded[filepath.Clean(path)] {
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
