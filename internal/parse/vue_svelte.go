package parse

import (
	"strings"

	"github.com/jasondillingham/leonard/internal/store"
)

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
//
// v0.42 (bughunt-5 preproc F1/F2): replaced the prior `.*?`-based
// regex with a scanner that respects quote + HTML-comment context.
// The old regex truncated on `</script>` inside string literals
// and incorrectly matched `<script>` inside `<!-- -->` comments.
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

// ExtractAstro handles Astro single-file components. The format
// has frontmatter (TypeScript) between `---` fences at the top,
// then HTML-like template + optional `<script>` blocks.
//
// v0.42 (bughunt-5 preproc F30): the frontmatter scan now respects
// JS string + template-literal context, so `\n---\n` inside a
// template literal doesn't truncate the body.
func ExtractAstro(path string, src []byte) ([]store.Symbol, error) {
	var out []store.Symbol
	if start, end := findAstroFrontmatter(src); start >= 0 {
		body := src[start:end]
		lineOffset := countNewlines(src[:start])
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
	// Plus any embedded <script>...</script> blocks in the template
	// body — same shape as Vue/Svelte. Astro client-side scripts
	// live here.
	scriptSyms, err := extractEmbeddedScript(path, src)
	if err != nil {
		return out, err
	}
	out = append(out, scriptSyms...)
	return out, nil
}

// extractEmbeddedScript walks every <script>...</script> block in
// src, runs each through ExtractTypeScript, and offsets each
// extracted Symbol's start_line/end_line by the script block's
// line offset within the SFC.
func extractEmbeddedScript(path string, src []byte) ([]store.Symbol, error) {
	blocks := findScriptBlocks(src)
	if len(blocks) == 0 {
		return nil, nil
	}
	var out []store.Symbol
	for _, b := range blocks {
		body := src[b.bodyStart:b.bodyEnd]
		lineOffset := countNewlines(src[:b.bodyStart])
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

// scriptBlock is a single <script>...</script> range within the SFC.
// bodyStart points at the first byte after the opening tag's `>`;
// bodyEnd points at the first byte of the closing `</script>` tag.
type scriptBlock struct {
	bodyStart int
	bodyEnd   int
}

// findScriptBlocks scans src for every well-formed <script>…</script>
// pair, respecting two contexts the previous regex got wrong:
//   - HTML comments (`<!-- … -->`): a `<script>` literal inside a
//     comment is NOT a tag.
//   - JS string + template literals inside the script body:
//     `const html = "</script>"` is NOT a closing tag.
//
// Returns the bodies in source order. Malformed input (unclosed
// tag, missing closing fence) is skipped silently — partial-doc
// extraction is preferable to no extraction.
func findScriptBlocks(src []byte) []scriptBlock {
	var out []scriptBlock
	i := 0
	for i < len(src) {
		// Skip HTML comments at the template level.
		if hasPrefixAt(src, i, "<!--") {
			end := indexFromAt(src, i+4, "-->")
			if end < 0 {
				break
			}
			i = end + 3
			continue
		}
		// Look for an opening `<script` token (case-insensitive on
		// the tag name).
		if !hasPrefixAt(src, i, "<script") {
			i++
			continue
		}
		// Find the closing `>` of the opening tag (respecting
		// attribute quoting — basic; Vue/Svelte/Astro tag attrs
		// rarely contain `>`).
		tagEnd := indexFromAt(src, i+7, ">")
		if tagEnd < 0 {
			break
		}
		// Reject self-closing `<script src="..." />` — no body to extract.
		if tagEnd > 0 && src[tagEnd-1] == '/' {
			i = tagEnd + 1
			continue
		}
		bodyStart := tagEnd + 1
		// Walk the body looking for the closing `</script>`, skipping
		// any occurrence inside a JS string or template literal.
		bodyEnd := scanForScriptClose(src, bodyStart)
		if bodyEnd < 0 {
			break
		}
		out = append(out, scriptBlock{bodyStart: bodyStart, bodyEnd: bodyEnd})
		// Advance past `</script>` (9 chars).
		i = bodyEnd + 9
	}
	return out
}

// scanForScriptClose returns the position of the closing `</script>`
// tag, starting the search at start, while tracking JS lexical
// context so a `</script>` substring inside a string/template
// literal doesn't end the body prematurely.
//
// Context tracked: single-quote string, double-quote string,
// template literal (backtick), single-line comment, multi-line
// comment. Regex literals aren't tracked — they very rarely contain
// `</script>` in practice; tradeoff documented.
//
// Returns -1 if no closing tag is found.
func scanForScriptClose(src []byte, start int) int {
	type ctx int
	const (
		ctxCode ctx = iota
		ctxStrDouble
		ctxStrSingle
		ctxTemplate
		ctxLineComment
		ctxBlockComment
	)
	c := ctxCode
	i := start
	for i < len(src) {
		switch c {
		case ctxCode:
			if hasPrefixAt(src, i, "</script>") {
				return i
			}
			switch src[i] {
			case '"':
				c = ctxStrDouble
			case '\'':
				c = ctxStrSingle
			case '`':
				c = ctxTemplate
			case '/':
				if i+1 < len(src) {
					switch src[i+1] {
					case '/':
						c = ctxLineComment
						i++
					case '*':
						c = ctxBlockComment
						i++
					}
				}
			}
		case ctxStrDouble:
			if src[i] == '\\' && i+1 < len(src) {
				i++ // skip the escaped char
			} else if src[i] == '"' {
				c = ctxCode
			}
		case ctxStrSingle:
			if src[i] == '\\' && i+1 < len(src) {
				i++
			} else if src[i] == '\'' {
				c = ctxCode
			}
		case ctxTemplate:
			if src[i] == '\\' && i+1 < len(src) {
				i++
			} else if src[i] == '`' {
				c = ctxCode
			}
			// NOTE: `${ … }` interpolation can contain code; v0.42
			// stays inside-template until backtick. A `</script>`
			// literally inside a `${}` block (very rare) would be
			// missed by this scanner but the alternative is much
			// more complex.
		case ctxLineComment:
			if src[i] == '\n' {
				c = ctxCode
			}
		case ctxBlockComment:
			if hasPrefixAt(src, i, "*/") {
				c = ctxCode
				i++
			}
		}
		i++
	}
	return -1
}

// findAstroFrontmatter returns the byte range of the frontmatter
// body (between the opening `---\n` and the closing `\n---`),
// respecting JS string/template literal context inside the body.
// Returns (-1, -1) when no well-formed frontmatter is present.
//
// The frontmatter must start at byte 0 of the file. Trailing
// whitespace after the opening fence is tolerated.
func findAstroFrontmatter(src []byte) (int, int) {
	// Opening fence: `---` followed by optional whitespace + newline.
	if !hasPrefixAt(src, 0, "---") {
		return -1, -1
	}
	openEnd := 3
	// Skip whitespace up to the newline.
	for openEnd < len(src) && (src[openEnd] == ' ' || src[openEnd] == '\t' || src[openEnd] == '\r') {
		openEnd++
	}
	if openEnd >= len(src) || src[openEnd] != '\n' {
		return -1, -1
	}
	bodyStart := openEnd + 1
	// Scan body looking for `\n---` while respecting JS string +
	// template literal context (so `\n---\n` inside a template
	// literal in the frontmatter doesn't truncate early).
	closer := scanForFenceClose(src, bodyStart)
	if closer < 0 {
		return -1, -1
	}
	return bodyStart, closer
}

// scanForFenceClose returns the position of `\n---` outside of any
// JS string/template literal, starting at start. Returns -1 if not
// found. Mirrors scanForScriptClose's context tracking but watches
// for the fence pattern instead of a closing tag.
func scanForFenceClose(src []byte, start int) int {
	type ctx int
	const (
		ctxCode ctx = iota
		ctxStrDouble
		ctxStrSingle
		ctxTemplate
		ctxLineComment
		ctxBlockComment
	)
	c := ctxCode
	i := start
	for i < len(src) {
		switch c {
		case ctxCode:
			if src[i] == '\n' && hasPrefixAt(src, i+1, "---") {
				return i
			}
			switch src[i] {
			case '"':
				c = ctxStrDouble
			case '\'':
				c = ctxStrSingle
			case '`':
				c = ctxTemplate
			case '/':
				if i+1 < len(src) {
					switch src[i+1] {
					case '/':
						c = ctxLineComment
						i++
					case '*':
						c = ctxBlockComment
						i++
					}
				}
			}
		case ctxStrDouble:
			if src[i] == '\\' && i+1 < len(src) {
				i++
			} else if src[i] == '"' {
				c = ctxCode
			}
		case ctxStrSingle:
			if src[i] == '\\' && i+1 < len(src) {
				i++
			} else if src[i] == '\'' {
				c = ctxCode
			}
		case ctxTemplate:
			if src[i] == '\\' && i+1 < len(src) {
				i++
			} else if src[i] == '`' {
				c = ctxCode
			}
		case ctxLineComment:
			if src[i] == '\n' {
				c = ctxCode
			}
		case ctxBlockComment:
			if hasPrefixAt(src, i, "*/") {
				c = ctxCode
				i++
			}
		}
		i++
	}
	return -1
}

func hasPrefixAt(src []byte, i int, prefix string) bool {
	if i < 0 || i+len(prefix) > len(src) {
		return false
	}
	return strings.EqualFold(string(src[i:i+len(prefix)]), prefix)
}

func indexFromAt(src []byte, start int, needle string) int {
	if start < 0 || start >= len(src) {
		return -1
	}
	rel := strings.Index(string(src[start:]), needle)
	if rel < 0 {
		return -1
	}
	return start + rel
}

func countNewlines(src []byte) int {
	n := 0
	for _, b := range src {
		if b == '\n' {
			n++
		}
	}
	return n
}
