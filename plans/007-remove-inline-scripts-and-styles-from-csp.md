# Plan 007: Remove inline scripts and styles from the CSP

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat a767b82..HEAD -- app.go app_test.go templates/app.html richtext.go static`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: MED
- **Depends on**: `plans/001-establish-complete-verification-gates.md`
- **Category**: security
- **Planned at**: commit `a767b82`, 2026-07-09

## Why this matters

The app currently has careful server-side escaping for rich preview content, but its Content Security Policy still allows any inline script and style. That means CSP would not provide much defense-in-depth if a future template change accidentally introduced an HTML injection. Moving CSS and JavaScript to same-origin static files lets the CSP remove `unsafe-inline` while preserving current UI behavior.

## Current state

Relevant files:

- `app.go` — embeds templates and sets security headers.
- `templates/app.html` — contains a large inline `<style>`, inline `<script>` blocks, and many `style="..."` attributes.
- `richtext.go` — escapes rich text before returning `template.HTML`.
- `app_test.go` — already tests security headers.
- `static/app.css` and `static/app.js` — do not exist yet.

Excerpts:

```go
// app.go:84-99
// securityHeaders wraps h with conservative, app-wide security response headers.
// HSTS is sent only when the app is served over HTTPS (https), so it never
// pins plain-HTTP local dev. The CSP allows the inline <style>/<script> the
// templates use plus the Hack Club asset host, and forbids framing.
func securityHeaders(h http.Handler, https bool) http.Handler {
	const csp = "default-src 'self'; " +
		"img-src 'self' https://assets.hackclub.com data:; " +
		"font-src https://assets.hackclub.com; " +
		"style-src 'self' 'unsafe-inline'; " +
		"script-src 'self' 'unsafe-inline'; " +
		"frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
```

```go
// app.go:195-198
type fieldKV struct {
	Name  string
	Value template.HTML
	// Rich marks a field rendered as Airtable-style formatted rich text (the
```

```go
// richtext.go:19-22
// renderRichText converts our embedded Markdown into the HTML an Airtable
// rich-text long-text field displays. The input is HTML-escaped before any
// formatting is applied, so user-submitted answers can never inject markup.
func renderRichText(s string) template.HTML {
```

```html
<!-- templates/app.html:9 -->
<style>

<!-- templates/app.html:267-286 -->
<script>
  // Localize any <time data-localtime> to the viewer's timezone.
  (function () { ... })();
</script>

<!-- templates/app.html:579-634 -->
<script>
  // Show the optional template box only when a question maps to a feedback
  // column ... and re-render sample previews.
</script>
```

Examples of inline style attributes currently present:

```html
<!-- templates/app.html:342 -->
<div class="btn-row" style="margin-top:1.1rem">

<!-- templates/app.html:554 -->
<input type="text" class="template-input" ... style="margin-top:0.5rem; display:none">
```

Repo conventions to match:

- The production Docker image relies on `//go:embed`; any new static files needed at runtime must be embedded into the Go binary.
- The app uses only the Go standard library for HTTP routing/static responses.
- The UI uses Hack Club brand tokens already defined in the template CSS; preserve the visual design while moving the CSS.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Check no inline styles/scripts remain | `rg -n "unsafe-inline|<style>|<script>|style=" app.go templates/app.html` | no `unsafe-inline`, no `<style>`, no inline `<script>` blocks, no `style=` attributes; external `<script src=...>` is OK if your regex reports it |
| App tests | `go test .` | exit 0 |
| Full local gate | `make test` | exit 0 after Plan 001 |
| Vet | `make vet` | exit 0 after Plan 001 |

## Scope

**In scope** (the only files you should modify):

- `app.go`
- `app_test.go`
- `templates/app.html`
- `static/app.css` (create)
- `static/app.js` (create)

**Out of scope** (do NOT touch):

- Rich-text rendering semantics in `richtext.go`; it is listed in the drift check only because CSP is defense-in-depth around `template.HTML` usage.
- Adding a frontend build system or npm dependency.
- Changing visual design or user flows.
- Replacing `box.innerHTML` with a different rendering strategy. This plan's goal is CSP hardening; server-rendered preview HTML can continue to be swapped in.

## Git workflow

- Branch: `advisor/007-remove-inline-csp-allowances`
- Commit message style: short imperative sentence, e.g. `Move inline assets behind strict CSP`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Embed and serve static assets

In `app.go`:

1. Change the embed declaration so it includes both templates and static files, for example:

   ```go
   //go:embed templates/*.html static/*
   var assetsFS embed.FS
   ```

   If you rename `templatesFS`, update `template.ParseFS` accordingly.

2. Add routes in `Routes()` for:
   - `GET /static/app.css`
   - `GET /static/app.js`

3. Serve the embedded files with correct content types:
   - CSS: `text/css; charset=utf-8`
   - JS: `text/javascript; charset=utf-8`

Use a tiny helper that reads from `assetsFS` and writes the file. Do not serve arbitrary user-controlled paths; only the two fixed routes above.

**Verify**: `go test .` → exits 0.

### Step 2: Move template CSS into `static/app.css`

Create `static/app.css` and move the entire CSS body from the current `<style>` block into it.

Then in `templates/app.html`:

1. Replace the inline `<style>...</style>` with:

   ```html
   <link rel="stylesheet" href="/static/app.css">
   ```

2. Replace every `style="..."` attribute with a named CSS class in `static/app.css`.
   - Examples: `.mt-lg`, `.mt-md`, `.center-card`, `.empty-flow`, `.danger-eyebrow`, `.sample-fields-name`, `.template-input`.
   - Prefer semantic class names over many one-off utility classes when the element has a clear role.
3. For the color swatches on the login page, use CSS classes or CSS custom properties rather than inline `background:` styles.
4. For the template input visibility, make `.template-input` hidden by default in CSS, and have JS toggle a class such as `.is-visible` instead of writing `style.display`.
5. For preview loading opacity, use a class such as `.is-loading` instead of JS writing `box.style.opacity`.

**Verify**: `rg -n "<style>|style=" templates/app.html` → no matches.

**Verify**: `go test .` → exits 0.

### Step 3: Move JavaScript into `static/app.js`

Create `static/app.js` containing the behavior from both existing inline scripts:

1. Localize `time[data-localtime]` elements.
2. Show/hide template inputs for templatable targets.
3. POST to `/forms/preview/samples` when mappings/templates change and swap the returned HTML into `#preview-samples`.
4. Preserve the sequence guard that ignores stale preview responses.

Update `templates/app.html` so the `foot` template includes:

```html
<script src="/static/app.js" defer></script>
```

or place the script near the end of the body without `defer`. There must be no inline `<script>...</script>` blocks left.

When moving JS, replace direct style writes with class toggles:

- `tmpl.classList.toggle('is-visible', TEMPLATABLE[sel.value])`
- `box.classList.add('is-loading')` / `box.classList.remove('is-loading')`

**Verify**: `rg -n "<script>" templates/app.html` → no matches. An external `<script src="/static/app.js" ...>` is expected and acceptable.

**Verify**: `go test .` → exits 0.

### Step 4: Tighten the CSP

In `app.go` `securityHeaders`, remove both `unsafe-inline` allowances.

Target CSP shape:

```go
const csp = "default-src 'self'; " +
    "img-src 'self' https://assets.hackclub.com data:; " +
    "font-src https://assets.hackclub.com; " +
    "style-src 'self'; " +
    "script-src 'self'; " +
    "frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
```

If fonts loaded by `@font-face` from `static/app.css` require a different directive, keep `font-src https://assets.hackclub.com` as shown. Do not add `unsafe-inline`, `unsafe-eval`, `data:` for scripts, or broad wildcards.

Update the comment above `securityHeaders` so it no longer says CSP allows inline style/script.

**Verify**: `go test .` → exits 0.

**Verify**: `rg -n "unsafe-inline" app.go templates/app.html static` → no matches.

### Step 5: Add/adjust tests

In `app_test.go`:

1. Update the existing CSP test to assert:
   - `Content-Security-Policy` contains `script-src 'self'`.
   - `Content-Security-Policy` contains `style-src 'self'`.
   - `Content-Security-Policy` does not contain `unsafe-inline`.
   - Existing `frame-ancestors 'none'` assertion still passes.
2. Add tests for `GET /static/app.css` and `GET /static/app.js`:
   - status 200.
   - expected content type.
   - response body contains a stable marker (`:root` for CSS, `data-localtime` or `preview-samples` for JS).
3. Keep rich-text escaping tests unchanged.

**Verify**: `go test . -run 'Test.*(Security|Static|CSP)'` → exits 0.

### Step 6: Run full verification

**Verify**: `make vet && make test` → exits 0 after Plan 001.

If Plan 001 has not landed in your branch, run:

```bash
go vet ./...
(cd fillout && go vet ./...)
(cd airtable && go vet ./...)
go test ./...
(cd fillout && go test ./...)
(cd airtable && go test ./...)
```

Expected result: all exit 0.

## Test plan

New/updated tests:

- `app_test.go`: CSP has no `unsafe-inline` and still includes existing hardening directives.
- `app_test.go`: embedded static CSS/JS routes return 200 with correct content types.

Manual/browser smoke test if a browser is available:

- Load dashboard and confirm styling is intact.
- Confirm local time text still changes for `<time data-localtime>`.
- On preview page, change a mapping target/template and confirm sample preview refreshes.

## Done criteria

All must hold:

- [ ] No inline `<style>` block remains in `templates/app.html`.
- [ ] No inline `<script>` block remains in `templates/app.html`; only external `/static/app.js` is loaded.
- [ ] No `style="..."` attributes remain in `templates/app.html`.
- [ ] CSP no longer contains `unsafe-inline`.
- [ ] `/static/app.css` and `/static/app.js` are embedded and served by fixed routes.
- [ ] Existing UI behavior is preserved by `static/app.js` and `static/app.css`.
- [ ] `go test .` exits 0.
- [ ] `make vet && make test` exits 0 after Plan 001.
- [ ] No files outside the in-scope list are modified.
- [ ] `plans/README.md` status row updated.

## STOP conditions

Stop and report back if:

- Removing inline style attributes would require a broad visual redesign rather than a mechanical class extraction.
- A browser test shows CSP blocks Hack Club font or image assets even with the target directives.
- Serving static files appears to require adding a new dependency or build system.
- Any rich-text escaping test starts failing; do not weaken escaping to satisfy CSP changes.

## Maintenance notes

After this lands, reviewers should reject new inline `<script>`, `<style>`, or `style="..."` additions unless they also include a deliberate CSP update and security rationale. Keep static assets dependency-free and embedded so the production Docker image remains self-contained.
