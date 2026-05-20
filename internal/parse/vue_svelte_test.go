package parse

import (
	"testing"
)

// TestExtractVue_ScriptBlockLineOffset locks in that Symbols from
// the script block carry SFC-relative line numbers, not script-
// block-relative.
func TestExtractVue_ScriptBlockLineOffset(t *testing.T) {
	src := []byte(`<template>
  <div>hi</div>
</template>

<script lang="ts">
interface Props {
  name: string
}
</script>
`)
	syms, err := ExtractVue("Greeter.vue", src)
	if err != nil {
		t.Fatalf("ExtractVue: %v", err)
	}
	var found bool
	for _, s := range syms {
		if s.Name == "Props" && s.Kind == "interface" {
			found = true
			if s.StartLine != 6 {
				t.Errorf("Props start_line = %d, want 6 (script-block-relative was 2, SFC-relative is 6)", s.StartLine)
			}
		}
	}
	if !found {
		t.Errorf("Props symbol not extracted from script block; got %d syms", len(syms))
	}
}

// TestExtractVue_MultipleScriptBlocks covers Vue 3 SFCs that use
// both <script setup> (Composition API) and a plain <script> (for
// `export default` etc.) — symbols from both should be merged.
func TestExtractVue_MultipleScriptBlocks(t *testing.T) {
	src := []byte(`<template><div>hi</div></template>

<script setup lang="ts">
interface SetupProps { name: string }
</script>

<script lang="ts">
interface ClassicShape { id: number }
</script>
`)
	syms, err := ExtractVue("Greeter.vue", src)
	if err != nil {
		t.Fatalf("ExtractVue: %v", err)
	}
	names := map[string]bool{}
	for _, s := range syms {
		names[s.Name] = true
	}
	if !names["SetupProps"] {
		t.Error("SetupProps missing from multi-script extraction")
	}
	if !names["ClassicShape"] {
		t.Error("ClassicShape missing from multi-script extraction")
	}
}

// TestExtractSvelte_NoScriptBlock confirms that a Svelte file
// without a <script> tag (template + style only) returns no
// symbols cleanly, not an error.
func TestExtractSvelte_NoScriptBlock(t *testing.T) {
	src := []byte(`<div class="card">hi</div>
<style>.card { color: red; }</style>
`)
	syms, err := ExtractSvelte("Card.svelte", src)
	if err != nil {
		t.Fatalf("ExtractSvelte: %v", err)
	}
	if len(syms) != 0 {
		t.Errorf("script-less file produced %d symbols, want 0", len(syms))
	}
}

// TestExtractVue_ClosingTagInStringLiteralIsIgnored pins bughunt-5
// preproc F1: the v0.26 regex truncated on `</script>` inside a
// string literal, losing every declaration after it. v0.42's
// context-aware scanner respects JS string lexical state.
func TestExtractVue_ClosingTagInStringLiteralIsIgnored(t *testing.T) {
	src := []byte(`<template><div>hi</div></template>

<script lang="ts">
const html = "</script><div>fake</div>";
interface AfterFalseClose { id: number; }
</script>
`)
	syms, err := ExtractVue("Page.vue", src)
	if err != nil {
		t.Fatalf("ExtractVue: %v", err)
	}
	names := map[string]bool{}
	for _, s := range syms {
		names[s.Name] = true
	}
	if !names["AfterFalseClose"] {
		t.Errorf("interface after string-embedded </script> missing; got %d syms with names %v", len(syms), names)
	}
}

// TestExtractVue_CommentedScriptIsIgnored pins bughunt-5 preproc F2:
// `<script>` inside an HTML comment must NOT be treated as a tag.
func TestExtractVue_CommentedScriptIsIgnored(t *testing.T) {
	src := []byte(`<template><div>hi</div></template>
<!--
<script lang="ts">
interface PhantomInComment { junk: string }
</script>
-->
<script lang="ts">
interface RealOne { id: number }
</script>
`)
	syms, err := ExtractVue("Page.vue", src)
	if err != nil {
		t.Fatalf("ExtractVue: %v", err)
	}
	names := map[string]bool{}
	for _, s := range syms {
		names[s.Name] = true
	}
	if names["PhantomInComment"] {
		t.Error("interface inside HTML comment was incorrectly extracted")
	}
	if !names["RealOne"] {
		t.Error("real interface outside the comment missing")
	}
}

// TestExtractAstro_FrontmatterFenceInsideTemplateLiteralIgnored
// pins bughunt-5 preproc F30: a `\n---\n` literal inside a
// template literal in the frontmatter must NOT close the body
// early.
func TestExtractAstro_FrontmatterFenceInsideTemplateLiteralIgnored(t *testing.T) {
	src := []byte("---\n" +
		"const markdown = `\n---\n# heading\n---\n`;\n" +
		"interface AfterFakeFence { id: number; }\n" +
		"---\n<div>hi</div>\n")
	syms, err := ExtractAstro("Page.astro", src)
	if err != nil {
		t.Fatalf("ExtractAstro: %v", err)
	}
	var found bool
	for _, s := range syms {
		if s.Name == "AfterFakeFence" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("interface after template-literal-embedded fence missing; got %d syms", len(syms))
	}
}
