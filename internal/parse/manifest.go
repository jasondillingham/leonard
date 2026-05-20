// Package parse — manifest dependency extraction.
//
// Each ExtractXxxManifest function emits one Symbol per declared
// dependency. Symbol shape: name = dep name, kind = "const",
// signature = "dependency <name>@<version>", exported = true,
// start_line = 1 (manifest formats aren't position-aware enough
// to track per-dep lines cheaply).
//
// Use case: Claude calls verify_symbol("react") and Leonard tells
// it whether the project declares react as a dependency, without
// the model needing to grep manifest files. Adding a dep that's
// not actually in package.json is a real failure mode this catches.
package parse

import (
	"encoding/json"
	"encoding/xml"
	"fmt"

	"github.com/pelletier/go-toml/v2"
	"golang.org/x/mod/modfile"

	"github.com/jasondillingham/leonard/internal/store"
)

// depSymbol builds a Symbol for one declared dependency. version
// can be empty (some manifest formats don't carry a constraint
// at the dep declaration site).
//
// Bughunt-5 perf F9: v0.36 used kind="const" here, which polluted
// the const namespace on monorepo projects — 200 workspaces × 35
// deps = 7000 const symbols mixed with real-source consts.
// v0.44 introduces kind="dependency" so manifest entries are
// distinguishable in find_symbol output, doctor reports, and
// MCP tool kind filters.
func depSymbol(path, name, version string) store.Symbol {
	sig := "dependency " + name
	if version != "" {
		sig = sig + "@" + version
	}
	return store.Symbol{
		FilePath:      path,
		Name:          name,
		QualifiedName: moduleQualifier(path) + "." + name,
		Kind:          "dependency",
		Signature:     sig,
		StartLine:     1,
		EndLine:       1,
		Exported:      true,
	}
}

// packageJSON is the minimum shape needed to walk dependency
// blocks. Each block's values are RawMessage because npm allows
// either a version string OR an object form (e.g.
// `"foo": { "version": "1.0", "registry": "..." }` — supported by
// pnpm/yarn extensions). v0.42 (bughunt-5 preproc F3) made the
// value handling polymorphic so an object-form dep doesn't nuke
// the entire file's symbols on json.Unmarshal failure.
type packageJSON struct {
	Dependencies         map[string]json.RawMessage `json:"dependencies"`
	DevDependencies      map[string]json.RawMessage `json:"devDependencies"`
	PeerDependencies     map[string]json.RawMessage `json:"peerDependencies"`
	OptionalDependencies map[string]json.RawMessage `json:"optionalDependencies"`
}

// ExtractPackageJSON walks dependencies / devDependencies /
// peerDependencies / optionalDependencies in a package.json file.
// Each entry becomes one Symbol.
func ExtractPackageJSON(path string, src []byte) ([]store.Symbol, error) {
	var p packageJSON
	if err := json.Unmarshal(src, &p); err != nil {
		return nil, fmt.Errorf("manifest: parse package.json: %w", err)
	}
	var out []store.Symbol
	for _, m := range []map[string]json.RawMessage{
		p.Dependencies, p.DevDependencies, p.PeerDependencies, p.OptionalDependencies,
	} {
		for name, raw := range m {
			out = append(out, depSymbol(path, name, npmVersion(raw)))
		}
	}
	return out, nil
}

// npmVersion extracts a printable version string from either form
// npm allows: a bare string (`"1.0.0"`) or an object whose
// `version` key carries the constraint (pnpm/yarn extensions). An
// object without a recognizable version field returns "" — the
// symbol is still emitted but the signature drops the @<version>.
func npmVersion(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var asStr string
	if err := json.Unmarshal(raw, &asStr); err == nil {
		return asStr
	}
	var asObj struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &asObj); err == nil && asObj.Version != "" {
		return asObj.Version
	}
	return ""
}

// cargoToml is the minimal Cargo.toml shape. The `[dependencies]`
// section value type is either a version string (`serde = "1"`) or
// an inline table (`serde = { version = "1", features = [...] }`),
// so we accept `any` and extract the version field generically.
type cargoToml struct {
	Dependencies      map[string]any `toml:"dependencies"`
	DevDependencies   map[string]any `toml:"dev-dependencies"`
	BuildDependencies map[string]any `toml:"build-dependencies"`
}

// ExtractCargoToml walks the dependency sections of a Cargo.toml
// file. Each entry becomes one Symbol. Workspace and feature
// declarations are not captured in v0.36.
func ExtractCargoToml(path string, src []byte) ([]store.Symbol, error) {
	var c cargoToml
	if err := toml.Unmarshal(src, &c); err != nil {
		return nil, fmt.Errorf("manifest: parse Cargo.toml: %w", err)
	}
	var out []store.Symbol
	for _, m := range []map[string]any{
		c.Dependencies, c.DevDependencies, c.BuildDependencies,
	} {
		for name, v := range m {
			out = append(out, depSymbol(path, name, cargoVersion(v)))
		}
	}
	return out, nil
}

// cargoVersion pulls a version string out of either form Cargo
// allows: a bare version (`"1.0"`) or an inline table with a
// `version` key (`{ version = "1.0", features = [...] }`).
func cargoVersion(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case map[string]any:
		if s, ok := x["version"].(string); ok {
			return s
		}
		if _, ok := x["path"].(string); ok {
			return "path"
		}
		if _, ok := x["git"].(string); ok {
			return "git"
		}
	}
	return ""
}

// ExtractGoMod parses a go.mod file via the canonical
// golang.org/x/mod/modfile package and emits one Symbol per
// `require` directive. Indirect requires get their `// indirect`
// status reflected in the signature.
func ExtractGoMod(path string, src []byte) ([]store.Symbol, error) {
	f, err := modfile.Parse(path, src, nil)
	if err != nil {
		return nil, fmt.Errorf("manifest: parse go.mod: %w", err)
	}
	out := make([]store.Symbol, 0, len(f.Require))
	for _, r := range f.Require {
		sym := depSymbol(path, r.Mod.Path, r.Mod.Version)
		if r.Indirect {
			sym.Signature += " (indirect)"
		}
		out = append(out, sym)
	}
	return out, nil
}

// pomXML is the slice of Maven POM we walk. Only top-level
// `<dependencies>` is followed; nested profile/plugin/etc.
// dependencies are skipped for v0.36 simplicity.
type pomXML struct {
	Dependencies struct {
		Dep []struct {
			GroupID    string `xml:"groupId"`
			ArtifactID string `xml:"artifactId"`
			Version    string `xml:"version"`
		} `xml:"dependency"`
	} `xml:"dependencies"`
}

// ExtractPomXml parses a Maven pom.xml. Each <dependency> entry
// becomes one Symbol. The symbol name is the artifactId (Maven's
// "what is this package called" identifier); the groupId.artifactId
// pair is folded into the signature for full identification.
func ExtractPomXml(path string, src []byte) ([]store.Symbol, error) {
	var p pomXML
	if err := xml.Unmarshal(src, &p); err != nil {
		return nil, fmt.Errorf("manifest: parse pom.xml: %w", err)
	}
	out := make([]store.Symbol, 0, len(p.Dependencies.Dep))
	for _, d := range p.Dependencies.Dep {
		sym := depSymbol(path, d.ArtifactID, d.Version)
		// Maven artifacts are namespaced by groupId, which is the
		// way real-world refs disambiguate (e.g. `com.fasterxml.
		// jackson.core:jackson-core`). Fold it into the signature.
		if d.GroupID != "" {
			sym.Signature = "dependency " + d.GroupID + ":" + d.ArtifactID
			if d.Version != "" {
				sym.Signature += "@" + d.Version
			}
		}
		out = append(out, sym)
	}
	return out, nil
}
