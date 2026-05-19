package mcp

// Tool input/output payloads for the v1 MCP surface. Field names and JSON
// tags match the schemas declared in DESIGN.md §4.3 / phase-1-brief.md.

// SymbolMatch is the wire-format symbol returned by verify_symbol and
// find_symbol. Note the deliberate flattening of store.Symbol's StartLine
// + FilePath into the snake_case names Claude sees.
type SymbolMatch struct {
	File          string `json:"file"`
	Line          int    `json:"line"`
	Signature     string `json:"signature"`
	Kind          string `json:"kind"`
	QualifiedName string `json:"qualified_name"`
}

// FileEntry is the wire-format file returned by list_files.
type FileEntry struct {
	Path      string `json:"path"`
	Language  string `json:"language"`
	SizeBytes int64  `json:"size_bytes"`
}

// VerifySymbolInput is the argument shape for verify_symbol.
type VerifySymbolInput struct {
	Name     string `json:"name" jsonschema:"the symbol name to look up (exact match)"`
	Kind     string `json:"kind,omitempty" jsonschema:"optional kind filter: function|method|type|const|var|interface"`
	Language string `json:"language,omitempty" jsonschema:"optional language filter (e.g. go, python, typescript)"`
}

// VerifySymbolOutput is the result of verify_symbol.
type VerifySymbolOutput struct {
	Exists  bool          `json:"exists"`
	Matches []SymbolMatch `json:"matches"`
}

// FindSymbolInput is the argument shape for find_symbol.
type FindSymbolInput struct {
	Query string `json:"query" jsonschema:"substring matched against symbol name and qualified name (case-insensitive)"`
	Kind  string `json:"kind,omitempty" jsonschema:"optional kind filter: function|method|type|const|var|interface"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum number of matches to return (0 = unlimited)"`
}

// FindSymbolOutput wraps the matches array. MCP requires structured tool
// output to be an object, so arrays get a single-field envelope.
type FindSymbolOutput struct {
	Matches []SymbolMatch `json:"matches"`
}

// ListFilesInput is the argument shape for list_files.
type ListFilesInput struct {
	Pattern  string `json:"pattern,omitempty" jsonschema:"optional glob matched against file path (path.Match syntax)"`
	Language string `json:"language,omitempty" jsonschema:"optional language filter (e.g. go, python, typescript)"`
}

// ListFilesOutput wraps the files array (see FindSymbolOutput note).
type ListFilesOutput struct {
	Files []FileEntry `json:"files"`
}
