# Bug Hunt #5 — preprocessors

## Summary

Probed the five preprocessor extractors (Vue/Svelte/Astro regex, Jupyter,
OpenAPI, manifest) against 60+ edge-case fixtures. Real-world dogfood worked
(Swagger petstore yielded all 19 endpoints + 6 schemas), but several
correctness gaps surfaced. The most significant: the regex-based Vue/Svelte
extractor mis-truncates `<script>` bodies whenever a literal `</script>`
appears inside a string or comment (Vue's standard rendering recommendation
explicitly warns about this), and `<script>` blocks INSIDE HTML comments are
parsed as live code. The manifest extractors are brittle — a single
non-string dependency value in `package.json` causes the entire file to be
rejected with an unmarshal error, losing every other dep along with it. The
OpenAPI extractor's qualified-name format (`module.GET /users/{id}`) contains
a literal space + braces that may not survive other Leonard subsystems.
Multiple "out of scope" gaps (Cargo workspace deps, Maven dependencyManagement,
OpenAPI 3.1 webhooks) are documented but appear acceptable for v0.x.

Scratch tests lived at
`internal/parse/bughunt5_scratch{,2,3}_test.go` for the investigation and
were removed after — fixtures are inlined in this doc.

## Findings

### F1 — Vue/Svelte regex truncates on `</script>` inside strings or comments

- **Severity:** medium
- **Reproducer:**
  ```vue
  <template></template>
  <script lang="ts">
  const html = "</script><script>alert(1)</script>";
  interface AfterTrap { id: number }
  </script>
  ```
  `scriptBlockRe.FindAllSubmatchIndex` returns **2** matches with bodies
  `"\nconst html = \""` and `"alert(1)"`. The real interface declared after
  the string (`AfterTrap`) is never seen by ExtractTypeScript.
- **Observed:** Only the symbol `html` (kind=const, line=3) is extracted.
  `AfterTrap` is dropped. Worse, fragments of the literal payload get
  treated as additional script blocks.
- **Expected:** Either skip script blocks whose body contains an unbalanced
  `</script>` inside a string, or document the limitation prominently. The
  regex is fundamentally insufficient for the HTML5 raw-text-element
  algorithm (a real HTML parser would terminate the script at the first
  `</script>` regardless of quoting context — Vue tooling does the same —
  but Vue *itself* warns developers to break up such strings with
  `<\/script>` or string concatenation for exactly this reason).
- **Suggested fix shape:** Document in a comment near the regex that string-
  embedded `</script>` will mis-truncate. A more robust pass would scan
  forward looking for `</script>` outside of `"…"`, `'…'`, and `` `…` ``
  contexts. Even cheaper: detect when a captured body contains an odd
  number of unmatched quotes and emit a parse-failure for that file.

### F2 — Vue/Svelte regex matches `<script>` blocks inside HTML comments

- **Severity:** medium
- **Reproducer:**
  ```vue
  <template></template>
  <!-- <script>commented out</script> -->
  <script lang="ts">
  interface RealProps { x: number }
  </script>
  ```
  `scriptBlockRe.FindAllSubmatchIndex` finds **2** script blocks. The first
  match is the commented-out content; ExtractTypeScript runs on
  `"commented out"` which luckily contains no symbol-shaped text but on a
  more contentful comment could emit phantom symbols.
- **Observed:** Real symbol still extracted (`RealProps` line 4), but the
  commented-out region is also fed through the TS parser. Phantom symbols
  from commented code are possible.
- **Expected:** Strip `<!-- ... -->` HTML comments before running the
  script regex, or require the regex to start after a non-comment context.
- **Suggested fix shape:** Pre-pass that elides HTML comments. The current
  regex is `(?s)<script([^>]*)>(.*?)</script>` — add a leading-comment
  strip with `(?s)<!--.*?-->` removal, or scan and skip commented ranges.
- **Out of scope:** v0.26 brief calls the extractor "v0 scope is the script
  symbols only" — comment handling could be deferred to a follow-up.

### F3 — `package.json` parse fails entirely on any non-string dependency value

- **Severity:** medium
- **Reproducer:**
  ```json
  { "dependencies": { "react": "^18.0", "weird": { "version": "1.0" } } }
  ```
  Returns `manifest: parse package.json: json: cannot unmarshal object into
  Go struct field packageJSON.dependencies of type string` and ZERO
  symbols. The legitimate `react` dep is lost along with the weird one.
- **Observed:** All deps drop, file recorded as parse failure.
- **Expected:** Tolerate the unusual shape — npm itself accepts object-form
  deps in some contexts (workspaces protocol, certain registry variants).
  Either silently skip the object-form dep and keep the string-form ones,
  or use `map[string]json.RawMessage` and decode each value defensively.
- **Suggested fix shape:** Change `packageJSON` field type from
  `map[string]string` to `map[string]json.RawMessage`; per-entry try-string-
  then-object similar to Cargo's `cargoVersion`.
- **Out of scope:** package.json doesn't formally allow object-valued deps
  in the standard dependencies block, but `pnpm.overrides`, `resolutions`,
  etc. do — those aren't part of the four blocks Leonard walks anyway.

### F4 — Jupyter cell line numbers are concatenated-stream-relative, not cell-relative

- **Severity:** low (documented limitation)
- **Reproducer:** 5-cell notebook, each cell = `def fN(): return N\n`.
  Symbols extracted at lines 1, 4, 7, 10, 13. None of those map to a real
  per-cell line. A 1000-cell notebook produces lines up to 1999.
- **Observed:** Line numbers point to no useful location in the .ipynb
  source — neither cell index nor in-cell offset.
- **Expected:** Per the code comment, this is acknowledged: "approximate
  but useful for navigation (clients reading raw .ipynb JSON wouldn't have
  meaningful line numbers anyway since they're per-cell)." But `find_symbol`
  returns `path:line` — when the line points nowhere in the .ipynb, the
  output is misleading.
- **Suggested fix shape:** Either (a) clamp all Jupyter line numbers to 1
  with a sentinel ("see notebook" comment), or (b) emit a synthetic
  `cell_index` field in the signature: `"def myFunc() [cell 2 line 3]"`.
- **Out of scope:** Won't matter for verify_symbol (just yes/no) but does
  affect find_symbol UX.

### F5 — OpenAPI HTTP method casing collision creates duplicate symbols

- **Severity:** low
- **Reproducer:**
  ```yaml
  paths:
    /a:
      GET: { summary: capslock }
      Post: { summary: titlecase }
      get: { summary: lowercase }
  ```
  ExtractOpenAPI emits 3 symbols, all named **`GET /a`** or **`POST /a`**
  (the `strings.ToUpper(m)` step normalizes both `GET` and `get` to `GET`).
  The 3 symbols share Name and QualifiedName.
- **Observed:** Two symbols both named "GET /a" with identical signature
  and qualified_name persisted to the symbols table.
- **Expected:** Per OpenAPI spec, HTTP methods are case-sensitive lowercase
  (per RFC 3986 / RFC 7230, HTTP method *names* are uppercase, but OpenAPI
  3.x specifies the path item field keys are lowercase). Treating `GET` and
  `Post` as endpoints is non-compliant — they should be ignored as unknown
  fields. Real-world specs occasionally have these typos.
- **Suggested fix shape:** Change `openapiHTTPMethods[strings.ToLower(k)]`
  to require exact-lowercase match: `openapiHTTPMethods[k]`. Document the
  reasoning in the comment.
- **Out of scope:** Whether to be permissive or strict here is a judgement
  call.

### F6 — OpenAPI qualified names contain spaces and braces

- **Severity:** informational
- **Reproducer:** ExtractOpenAPI on the Swagger petstore emits symbols
  with `Name="GET /pet/{petId}"` and `QualifiedName="petstore.GET /pet/{petId}"`.
- **Observed:** The qualified name contains a literal space and curly braces.
  If any downstream subsystem (MCP server, claims ledger, verify_symbol
  string matching) splits on whitespace or treats `{` specially, lookups
  will fail.
- **Expected:** Maybe acceptable as a v0 trade-off, but the convention
  elsewhere in Leonard is dotted identifiers — the OpenAPI symbols are
  the only ones that violate that.
- **Suggested fix shape:** Replace the qualified-name format with something
  like `petstore.GET_pet_petId` (slashes → underscores, braces stripped),
  or quote the path component. Keep the human-readable `Name` field as-is.
- **Out of scope:** Verify against the MCP server's `find_symbol` to see if
  this causes practical problems.

### F7 — OpenAPI 3.1 `webhooks` field is not extracted

- **Severity:** low
- **Reproducer:**
  ```json
  { "openapi": "3.1.0", "webhooks": { "newPet": { "post": {} } },
    "components": { "schemas": { "X": {} } } }
  ```
  Only the `X` schema is emitted. The `newPet` webhook with its `POST`
  operation is silently dropped.
- **Observed:** 1 symbol (the schema). Webhook operations missing.
- **Expected:** OpenAPI 3.1 added top-level `webhooks` as a peer of `paths`
  per the spec. Leonard's extractor walks `paths` and `components.schemas`
  only.
- **Suggested fix shape:** Add a `webhooks` walker that emits symbols
  shaped like `WEBHOOK newPet.POST` or `POST webhook:newPet`. Same shape
  as the paths walker.
- **Out of scope:** 3.1 spec is relatively new; check how common in-repo
  3.1 specs are before prioritizing.

### F8 — OpenAPI extractor only sees the first document of a multi-document YAML

- **Severity:** informational
- **Reproducer:**
  ```yaml
  paths:
    /first: { get: {} }
  ---
  paths:
    /second: { get: {} }
  ```
  Only `GET /first` is emitted; `/second` is dropped.
- **Observed:** Standard `yaml.Unmarshal` behavior — first document wins.
- **Expected:** Document the limitation. Multi-document YAML in an OpenAPI
  spec is extremely rare but not impossible.
- **Suggested fix shape:** Either accept as-is (note in comment) or use a
  `yaml.NewDecoder(...).Decode` loop to walk all docs.

### F9 — Astro frontmatter regex captures trailing `\r` on Windows CRLF files

- **Severity:** low
- **Reproducer:**
  ```
  ---\r\ninterface CRLFAstro { x: number }\r\n---\r\n<html></html>\r\n
  ```
  `astroFrontmatterRe := regexp.MustCompile(`(?s)\A---\s*\n(.*?)\n---`)` —
  the `\n` matches LF only, so the captured body ends with a stray `\r`.
- **Observed:** TypeScript extractor copes (symbol still emitted), but the
  trailing CR is fragile and could break the TS scanner on edge cases.
- **Expected:** Regex should be `\r?\n` for the fences.
- **Suggested fix shape:** Replace each `\n` in the frontmatter regex with
  `\r?\n`. Same fix applies if anyone adds CRLF awareness to the script
  block regex.
- **Out of scope:** Vue/Svelte's script regex uses `(.*?)` with `(?s)` so
  CRLF inside the body is fine. The opening `<script>` tag doesn't include
  any newline anchoring, so CRLF in the header is fine too — only the
  Astro frontmatter is affected.

### F10 — Astro frontmatter regex requires `\n---` not `---\Z` — frontmatter without trailing newline before fence is missed

- **Severity:** low
- **Reproducer:**
  ```
  ---\ninterface X { y: number }---\n<html></html>\n
  ```
  (no newline between the body and the closing fence)
  No frontmatter match (regex requires `\n---`). The interface is missed.
- **Observed:** No symbols extracted; valid Astro syntactically (though
  unusual).
- **Expected:** Either be permissive or document.
- **Out of scope:** Vanishingly rare in practice.

### F11 — Astro frontmatter regex requires fence at exact byte 0, no BOM/whitespace allowed

- **Severity:** low
- **Reproducer:** File with a leading blank line:
  ```
  \n---\ninterface NotAtStart { x: number }\n---\n<html></html>\n
  ```
  `\A---` anchor prevents the match. No frontmatter extracted.
- **Observed:** No symbols.
- **Expected:** Astro spec says frontmatter must be at the very top — the
  current behavior matches the spec. BOM is the practical concern: a UTF-8
  BOM-prefixed Astro file would also fail to match.
- **Suggested fix shape:** Probably leave as-is; just confirm BOM-handling
  in the indexer's read path. (BOMs in `.vue` files: T45 confirmed Vue
  extraction still works because the `<script>` tag is not anchored to
  position 0.)

### F12 — Vue/Svelte script regex matches `<script type="application/json">` data islands

- **Severity:** low
- **Reproducer:**
  ```vue
  <template></template>
  <script type="application/json">{"some": "data"}</script>
  <script lang="ts">interface RealProps { x: number }</script>
  ```
  Both blocks routed through ExtractTypeScript. The JSON data block is
  parsed as TypeScript.
- **Observed:** No symbols emitted from the JSON island in this test (TS
  parser tolerated the object literal). But a JSON-LD or similar block
  with identifier-like keys could produce phantom symbols.
- **Expected:** Skip script blocks with `type="application/json"`,
  `type="application/ld+json"`, `type="text/template"`, etc. The
  captured group 1 (attrs) is available but the code ignores it.
- **Suggested fix shape:** In `extractEmbeddedScript`, check the attrs
  group; skip if the type attribute indicates non-JS content. Same regex,
  one extra string check.
- **Out of scope:** Mostly a theoretical issue.

### F13 — `<script src="...">` with empty body produces no symbols (correct), but the regex pattern requires both `<script>` and `</script>` to match

- **Severity:** informational (verifies expected behavior)
- **Reproducer:**
  ```vue
  <script src="./external.js"></script>
  <script lang="ts">interface BodyHere { x: number }</script>
  ```
  `<script src="...">` becomes match with empty body — body extracted is
  the empty string, ExtractTypeScript returns no symbols. Real symbol from
  body block is still extracted.
- **Observed:** Works fine.
- **Expected:** Works fine.

### F14 — XHTML-style self-closing `<script />` is NOT matched by the regex

- **Severity:** informational
- **Reproducer:**
  ```vue
  <script lang="ts" src="x.js" />
  <script lang="ts">interface RealProps { x: number }</script>
  ```
  Only the second block matched. The first `<script ... />` is invalid
  HTML5 but legal XHTML. Not matching it is correct (no body to extract).

### F15 — Cargo workspace dependencies (`[workspace.dependencies]`, `ws = { workspace = true }`) not captured / mis-emitted

- **Severity:** low (documented gap)
- **Reproducer:**
  ```toml
  [workspace.dependencies]
  foo = "1"
  bar = { version = "2" }
  ```
  Zero symbols extracted. The `[workspace.dependencies]` section is in
  the parent Cargo.toml of a workspace and is the canonical way to share
  versions across crate members.
- **Observed:** No symbols.
- **Expected:** Either walk workspace.dependencies or document.
- **Suggested fix shape:** Add `[workspace.dependencies]` to the
  `cargoToml` struct.

Additionally, inline workspace inheritance:
```toml
[dependencies]
ws = { workspace = true }
```
emits a symbol with name=`ws` and version=empty (no version is
extractable since the true version lives in the parent). Acceptable, but
the signature `dependency ws` is not very informative — could include
`(workspace)` for clarity.

### F16 — Cargo target-specific dependencies (`[target.*.dependencies]`) not captured

- **Severity:** low
- **Reproducer:**
  ```toml
  [target.'cfg(unix)'.dependencies]
  libc = "0.2"

  [target.x86_64-pc-windows-msvc.dependencies]
  winapi = "0.3"
  ```
  Zero symbols. Real-world Cargo.toml files routinely declare
  platform-specific deps this way.
- **Observed:** No symbols.
- **Expected:** Walk all `target.*.dependencies` sections.

### F17 — pom.xml `<dependencyManagement>`, `<profiles>`, `<plugins>` not walked

- **Severity:** low (documented gap)
- **Reproducer:** Test fixture T20:
  ```xml
  <project>
    <dependencies>            <!-- 1 dep extracted -->
    <dependencyManagement>    <!-- NOT extracted -->
    <profiles>                <!-- NOT extracted -->
  </project>
  ```
  Only the top-level `<dependencies>/<dependency>` entries become symbols.
  `<dependencyManagement>` and profile-scoped deps are silently dropped.
- **Observed:** Only `top` extracted (the toplevel dep); `dm-only` and
  `profile-only` lost.
- **Expected:** The comment in `manifest.go` explicitly calls this out:
  "Only top-level `<dependencies>` is followed; nested profile/plugin/etc.
  dependencies are skipped for v0.36 simplicity." So this is documented.
  However, **`<dependencyManagement>` in particular is extremely common**
  in Spring Boot projects (the parent POM pattern) — those projects will
  appear to have fewer deps than they really do.

### F18 — go.mod with v2+ module-path-suffix-less version fails to parse

- **Severity:** low
- **Reproducer:**
  ```
  module example.com/x
  go 1.21
  require (
    github.com/a/b v1.0.0
    github.com/c/d v2.0.0 // indirect
  )
  ```
  Returns `manifest: parse go.mod: go.mod:7:2: require github.com/c/d:
  version "v2.0.0" invalid: should be v0 or v1, not v2`. ZERO symbols.
  The valid v1 dep is lost too.
- **Observed:** modfile.Parse with the third arg `nil` enforces v0/v1 SIV
  compatibility. Real go.mod files in production routinely have v2+ deps
  with `/v2` module path suffix — those would parse fine. But malformed
  go.mod with v2+ versions without the suffix (legal in Go's own modfile
  parser if you pass a permissive `version func`) fail.
- **Expected:** Pass a version validator that accepts more shapes, or
  catch the error and emit a parse failure but still try to extract what
  it can.
- **Suggested fix shape:** modfile.Parse third arg accepts a `func(path,
  version string) error` — passing a no-op validator would skip the SIV
  check. Or use `modfile.ParseLax`.
- **Out of scope:** Not a common shape — real Go projects with v2+ deps
  use the `/v2` module path suffix correctly.

### F19 — go.mod `replace` and `exclude` directives are not emitted as symbols

- **Severity:** informational
- **Reproducer:** A go.mod with `replace` and `exclude` lines — these are
  parsed by modfile but only `require` entries are walked.
- **Observed:** Only `require` deps emitted.
- **Expected:** If `verify_symbol("forked/b")` is meant to confirm a dep is
  in use, `replace` directives could be relevant. But this is an
  informational gap, not a bug — the file is correctly parsed, just not
  fully enumerated.

### F20 — Jupyter robustness: bad source types correctly fail but produce no partial results

- **Severity:** informational
- **Reproducer:**
  - `source: 42` (number) → `jupyter: decode cell source: expected string or array, got: 42`, zero symbols
  - `source: {"weird":"shape"}` (object) → same error
  - `source: null` → empty string returned (decodeCellSource short-circuits on len(raw)==0… wait, `null` is 4 bytes `null`, len is 4. Let me check.) Actually `source: null` returns 0 syms, no error — because `json.Unmarshal("null", &str)` succeeds, leaving str=""; OR because `[null]` in decodeCellSource sets the cellSrc to ""; either way it's quiet.

  Importantly: a notebook with ONE bad cell and 99 good cells aborts with an error on the first bad cell. The 99 good ones are lost.
- **Observed:** Single bad cell sinks the whole file.
- **Expected:** Could be more tolerant — skip bad cells with a warning,
  continue with the rest. Symmetric to the "tolerate one parse error
  per file" approach in the indexer.

### F21 — Jupyter UTF-16 / BOM files fail with cryptic JSON error

- **Severity:** informational
- **Reproducer:**
  - UTF-8 BOM (`0xef 0xbb 0xbf` + `{"cells":[]}`): error `invalid character 'ï' looking for beginning of value`
  - UTF-16 LE BOM (`0xff 0xfe`): same.
- **Observed:** Clean parse failure with no panic. The error message is
  not actionable ("invalid character 'ï'" doesn't tell the user the file
  is BOM-prefixed).
- **Expected:** Strip BOM before json.Unmarshal. Real-world .ipynb files
  written by Windows tools can have BOMs.

### F22 — Vue line offset arithmetic is correct under UTF-8 multibyte content

- **Severity:** informational (verified working)
- **Reproducer:**
  ```vue
  <template>
    <div>こんにちは世界</div>
  </template>

  <script lang="ts">
  interface UnicodeProps { name: string }
  </script>
  ```
  `UnicodeProps` correctly emerges at line 6.
- **Observed:** The line offset counts newlines (bytes equal to `\n`),
  which is multibyte-safe since `\n` is single-byte and UTF-8 continuation
  bytes start with `10xxxxxx`. No false offset.

### F23 — Vue line offset is also correct for very large files

- **Severity:** informational
- **Reproducer:** 100 lines of HTML comments then a script block.
  `Far` interface extracted at line 102 as expected.

### F24 — Vue/Svelte unclosed `<script>` block silently extracts nothing

- **Severity:** low
- **Reproducer:**
  ```
  <template></template>
  <script lang="ts">
  interface OrphanProps { x: number }
  ```
  (no closing tag) → zero symbols, no error.
- **Observed:** Regex requires both opening and closing tags, so unclosed
  blocks are silently ignored.
- **Expected:** Probably fine, but a malformed SFC produces no symbols and
  no failure indication. Could emit a parse failure to surface the issue.

### F25 — defaultSkipDirs does not include `examples/`, `samples/`, `e2e/`, etc.

- **Severity:** informational
- **Reproducer:** A monorepo with `examples/foo/package.json` and a root
  `package.json` — both are indexed, both contribute dep symbols. The
  examples' deps pollute the root project's dep symbols.
- **Observed:** `defaultSkipDirs` only has `.git`, `.mypy_cache`, `.next`,
  `.nuxt`, `.pytest_cache`, `.ruff_cache`, `.tox`, `.venv`, `__pycache__`,
  `build`, `dist`, `node_modules`, `target`, `vendor`, `venv`. Nothing about
  `examples/`, `e2e/`, `samples/`, `demo/`.
- **Expected:** Indexer policy decision — examples should probably be
  indexed (they're real code), but the manifest extractor's coarse "every
  package.json gets one symbol per dep" behavior means a multi-package
  monorepo blasts the symbols table with redundant deps from
  examples / playgrounds.
- **Suggested fix shape:** This is more about how `verify_symbol("react")`
  is implemented than about the extractor itself — if it's name-based,
  any one of the package.json files showing react=true is enough. If it's
  qualified-name-based, the user has to know which package.json they care
  about.

### F26 — `defaultSkipDirs` correctly blocks `node_modules/foo/package.json`

- **Severity:** informational (verified working)
- **Reproducer:** node_modules is in defaultSkipDirs (line 58 of indexer.go).
  A package.json nested inside is never indexed. Verified by inspection.

### F27 — Files larger than 8 MiB are rejected with parseFailure

- **Severity:** informational (verified working)
- **Reproducer:** A 200 MB .ipynb would hit `maxIndexedFileBytes = 8 << 20`
  (line 562 of indexer.go) and be rejected before reading the file body.
  Clean failure path.

### F28 — Jupyter / OpenAPI / manifest extractors are stateless and concurrent-safe

- **Severity:** informational (verified)
- **Reproducer:** 10 goroutines simultaneously calling ExtractJupyter on
  the same source produced identical, correct results.

### F29 — Astro `<script>` blocks AFTER frontmatter: line offsets compose correctly

- **Severity:** informational (verified)
- **Reproducer:**
  ```astro
  ---
  interface FrontProps { id: number }
  ---
  <html>
  <script>
  interface ScriptProps { id: number }
  </script>
  </html>
  ```
  `FrontProps` at line 2 (frontmatter-relative + opening-fence offset of 1)
  and `ScriptProps` at line 6 (correct). Both extractors run independently
  and merge results. Works as designed.

### F30 — Astro extractor's frontmatter regex with content `\n---\n` inside a template literal: non-greedy match terminates early

- **Severity:** low
- **Reproducer:**
  ```astro
  ---
  const block = `
  ---
  `;
  interface TrickFM { id: number }
  ---
  <html></html>
  ```
  The non-greedy `.*?` in `astroFrontmatterRe` matches up to the FIRST
  `\n---`, which is inside the template literal. The body captured is
  ``const block = `\n`` (truncated). `interface TrickFM` is missed entirely,
  and the partial body fed to ExtractTypeScript leaves `block` as a const.
- **Observed:** Only `block` extracted (kind=const, line=2). `TrickFM`
  is lost.
- **Expected:** Same root-cause as F1 (regex can't recognize string
  contexts). Document or fix.

### F31 — Same dep declared in `dependencies` and `devDependencies` produces two identical-name symbols

- **Severity:** informational
- **Reproducer:** T18 — `react` in both blocks. Two symbols emitted, both
  named `react`, with qualified_name `package.react`. The signatures
  differ by version.
- **Observed:** Duplicate symbols on the same name + qualified_name.
- **Expected:** Likely fine for verify_symbol (just need one match), but
  the symbols table will have ambiguous rows.

### F32 — package.json `bundledDependencies` array form not captured

- **Severity:** informational
- **Reproducer:** T40 — `bundledDependencies: ["b", "c"]` not extracted.
  Comment in code says "older `bundledDependencies` arrays aren't carried."
  Documented.

### F33 — package.json `workspaces` array references not captured

- **Severity:** informational
- **Reproducer:** T49 — `workspaces: ["packages/*"]` not extracted. Each
  packages/*/package.json is indexed independently via the walker, so the
  workspace-glob entries aren't symbols themselves — this is fine.

### F34 — Maven project files without a top-level `<dependencies>` block produce nothing

- **Severity:** informational
- **Reproducer:** Spring Boot `<parent>` POMs that inherit deps from a
  parent POM and only declare `<dependencyManagement>` entries — they
  appear empty to Leonard.

## Things that worked

- **Real-world dogfood:** Ran ExtractOpenAPI against the Swagger petstore
  v3.0.4 YAML (839 lines). All 19 endpoints + 6 schemas extracted with
  correct names and signatures.
- **Vue/Svelte multi-script blocks:** Vue 3's `<script setup>` + classic
  `<script>` pattern — both extracted, symbols merged.
- **CRLF in Vue script bodies:** the `(?s)` regex flag makes `.` match CRLF
  fine, no offset corruption.
- **UTF-8 multibyte content before a script block:** line offsets compute
  via newline-counting (single-byte `\n`), so multibyte content doesn't
  desync the offset.
- **Jupyter array-form source decoding:** correctly joins per-line strings.
- **Jupyter cell_type filtering:** markdown/raw/unknown-type cells correctly
  skipped.
- **Jupyter concurrent stateless calls:** 10 goroutines × same source =
  identical, correct results.
- **OpenAPI JSON & YAML format coverage:** both parse paths work; YAML
  `map[any]any` normalization handles non-string-keyed maps gracefully
  (non-string keys dropped, not error).
- **OpenAPI Swagger 2.0 `definitions` fallback:** verified, extracts.
- **OpenAPI 3.x `components.schemas` walking:** verified.
- **OpenAPI sort-stability:** path names + method names are sorted before
  emission, so symbol order is deterministic across runs.
- **Cargo inline-table dependency parsing:** `{ version, features, path,
  git }` all handled correctly, with path/git falling back to special
  markers when no version is given.
- **Cargo `[dependencies.foo]` table syntax:** works (parsed identically
  to inline-table by go-toml).
- **Cargo renamed-package deps:** `serde2 = { package = "serde", version =
  "1" }` correctly emits a symbol named `serde2` (Leonard's view of the
  manifest matches the rename, but `verify_symbol("serde")` would miss
  it — documented in F20 of original brief).
- **pom.xml groupId folding:** signatures include `groupId:artifactId@version`
  when groupId is present.
- **go.mod indirect requires:** correctly flagged in signature.
- **Bom-prefixed Vue file:** still extracts symbols (the regex isn't
  position-anchored, so the BOM bytes precede the script tag harmlessly).
- **8 MiB file size cap:** confirmed in indexer.go:562; pathological large
  notebooks would be rejected before parsing.
- **defaultSkipDirs blocks node_modules:** confirmed; nested package.json
  inside node_modules is never indexed.

## Open questions

- **F1/F30 (regex truncation):** is there appetite to swap to a HTML-aware
  parser (e.g. `golang.org/x/net/html`) for Vue/Svelte/Astro, or to keep
  the regex and just document the failure modes? Adding a dependency was
  off-limits this round but worth flagging for the fix round.
- **F5 (HTTP method casing):** strict vs permissive? OpenAPI spec is
  unambiguous (lowercase), but real-world specs sometimes have casing
  typos. Leonard's current behavior is permissive AND produces collisions —
  that combination is the worst of both worlds.
- **F6 (qualified names with spaces/braces):** does the MCP server's
  `find_symbol(qualified_name)` lookup handle these names cleanly? This
  needs an end-to-end probe — out of scope for this lane (would require
  spinning up the MCP server, which is bug-hunt-1's territory).
- **F25 (`examples/` not in skip dirs):** policy call — is the goal to
  index everything (current behavior) or to default to the project's main
  package set?
- **Jupyter line attribution (F4):** is the find_symbol UX of "line points
  nowhere" considered a regression worth fixing, or accepted as the
  documented limitation?
- **Vue 3 `<script setup>` macros** (`defineProps`, `defineEmits`,
  `defineExpose`) — these are compiler macros that the TS scanner would
  see as function calls. Did not probe — they're not symbol-declaring
  per se, but a user might expect `verify_symbol("defineProps")` to be
  no-op. Out of scope.
- **CRLF in the script block's opening tag** (`<script lang="ts"\r\n>`) —
  the regex's `[^>]*` allows newlines, so this works. Not probed
  exhaustively.
