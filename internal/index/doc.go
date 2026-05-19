// Package index walks the project tree, dispatches files to language-specific
// parsers, and persists extracted symbols into the store. Incremental:
// per-file hashes drive re-parse decisions.
package index
