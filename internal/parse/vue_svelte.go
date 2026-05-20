package parse

import (
	"regexp"

	"github.com/jasondillingham/leonard/internal/store"
)

// scriptBlockRe matches the contents of a single <script>...</script>
// block in a Vue or Svelte SFC. Captures:
//   group 1: opening tag attribute text (so a future refinement can
//            tell `lang="ts"` from JS — v0.26 ignores it and treats
//            every script block as TypeScript)
//   group 2: the script body
//   prefix: count of bytes before the body (used to compute the
//           line-number offset Symbols need on the way out)
//
// (?s) lets `.` match newlines. The non-greedy `.*?` between
// the tag and `</script>` matches a single block. Multiple
// <script>...</script> blocks (Vue 3 SFCs sometimes have one
// `<script setup>` plus a regular `<script>`) are walked
// independently below.
var scriptBlockRe = regexp.MustCompile(`(?s)<script([^>]*)>(.*?)</script>`)

// ExtractVue extracts TypeScript/JavaScript symbols from the
// <script> block(s) of a Vue Single-File Component. Template and
// style sections are intentionally ignored — v0.26 scope is the
// script symbols only. A Vue 3 SFC may have two script blocks
// (`<script setup>` plus a classic `<script>` for `export default`);
// both are extracted and their symbols are merged.
//
// Line numbers in returned Symbols are adjusted to the SFC's line
// space, not the script block's, so `find_symbol` lines point at
// the actual position in the .vue file.
func ExtractVue(path string, src []byte) ([]store.Symbol, error) {
	return extractEmbeddedScript(path, src)
}

// ExtractSvelte is the same shape as ExtractVue — Svelte's SFCs
// use the same <script>...</script> wrapping (with optional
// `lang="ts"` attr). v0.26 treats Vue and Svelte identically at
// the extraction level; either grammar could later get its own
// tree-sitter parser for template-level symbols.
func ExtractSvelte(path string, src []byte) ([]store.Symbol, error) {
	return extractEmbeddedScript(path, src)
}

// extractEmbeddedScript walks every <script>...</script> block in
// src, runs each through ExtractTypeScript, and offsets each
// extracted Symbol's start_line/end_line by the script block's
// line offset within the SFC.
func extractEmbeddedScript(path string, src []byte) ([]store.Symbol, error) {
	matches := scriptBlockRe.FindAllSubmatchIndex(src, -1)
	if len(matches) == 0 {
		return nil, nil
	}
	var out []store.Symbol
	for _, m := range matches {
		// m[0..1] is the whole match, m[2..3] is group 1 (attrs),
		// m[4..5] is group 2 (body).
		bodyStart := m[4]
		bodyEnd := m[5]
		body := src[bodyStart:bodyEnd]
		// Count newlines up to bodyStart to compute the line offset
		// (1-based). A script block starting at byte 0 with no
		// leading content has offset 0 — Symbols already use 1-based
		// lines so we add lineOffset (which is the number of newlines
		// PRECEDING the body, NOT including the body's own first line).
		lineOffset := 0
		for _, b := range src[:bodyStart] {
			if b == '\n' {
				lineOffset++
			}
		}
		// ExtractTypeScript treats its src as a standalone .ts file
		// — line numbers are 1-based relative to that. We adjust
		// each Symbol's lines by lineOffset.
		syms, err := ExtractTypeScript(path, body)
		if err != nil {
			return out, err
		}
		for i := range syms {
			syms[i].StartLine += lineOffset
			syms[i].EndLine += lineOffset
		}
		out = append(out, syms...)
	}
	return out, nil
}
