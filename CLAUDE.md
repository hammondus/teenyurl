# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A self-hosted URL shortener. One Go binary, one dependency (`rsc.io/qr`), no
database, and no third-party JavaScript. Everything is `package main` in the
repository root; `web/` holds templates and static assets that `//go:embed`
compiles into the binary.

Read [DESIGN-DECISIONS.md](DESIGN-DECISIONS.md) before changing anything
structural. It records the rejected alternatives, not only the chosen ones,
and it is the file to update when a decision changes.

## Commands

```
make test      # go vet ./... && go test -race ./...
make build     # binary for this machine
make run       # build, then run against ./data, reading .env
make release   # static linux/arm64 binary in dist/
make docker-build
```

To run one test: `go test -race -run TestRedirectExpiredLink ./...`. Keep
`-race`: the redirect path takes a read lock while admin writes take the write
lock, and that split is what the flag proves.

`make run` needs a `.env` with `TEENYURL_ADMIN_PASSWORD` set to 12 or more
characters. The service refuses to start without one.

## Architecture

`main.go` reads the whole configuration from the environment, builds the
`Store`, `renderer`, and `proxyTrust`, and wires them into a `server`. There
are no flags. `server` (in `redirect.go`) holds those dependencies plus a `now
func() time.Time` field, which is how tests decide expiry instead of racing it.

**Storage (`store.go`, `links.go`).** Links live in a `map[string]*Link` in
memory and persist to two files with deliberately different durability:

- `links.jsonl` — append-only, fsynced on every write. Each record is the
  *complete* state of a link, never a delta, so replay is "last record wins per
  code". Delete appends a record with `Deleted` set; without that tombstone,
  replay resurrects the link from its create record. A truncated final line is
  dropped, but a parse failure on any earlier line stops startup.
- `clicks.json` — a whole-file snapshot written through a temporary file and a
  rename, on a timer. Counters are last-write-wins state, so appending them to
  a log would be pure garbage growth.

Click counts are a map parallel to `links`, not a field on `Link`, so a
redirect needs only a read lock and an atomic increment. Never move them onto
`Link`; that would put every redirect behind the write lock.

Both files are safe to copy while the service runs, and the format is also the
import and export format.

**Routing (`redirect.go`).** One wildcard, `GET /{code}`, serves both the
redirect and the preview page: in a URL path a `+` is a literal character, so
`docs+` arrives intact and the handler strips the suffix. Literal patterns
(`/healthz`, `/robots.txt`, `/static/`, `/admin`) always beat the wildcard
whatever the registration order, and `reservedCodes` in `links.go` stops an
alias from claiming one anyway. Redirects are 302 with `Cache-Control:
no-store` — a 301 would be cached permanently and take the click count and the
editable destination with it.

**Auth (`auth.go`, `admin.go`).** One password for the whole write surface, no
user accounts. The password is SHA-256'd on both sides and compared with
`subtle.ConstantTimeCompare`; there is no password database to protect, so
length is the strength. Sessions and login-attempt buckets are in-memory maps,
pruned on access, and both are lost on restart by design. `guard` wraps every
admin handler: it checks the session, checks the form token on POSTs, and sets
the admin cache and framing headers in one place.

**`clientip.go` is a security control, not logging.** The login rate limiter
keys on `clientIP`, so an attacker who can forge `X-Forwarded-For` resets their
own bucket. The header is honoured only when the immediate peer falls inside
`TEENYURL_TRUSTED_PROXIES`, and the walk stops at the first untrusted or
malformed hop.

**Rendering (`render.go`).** Each page template is parsed together with
`base.html` *separately*, because Go templates share one namespace and parsing
them all at once leaves every page fighting over the name `content`. Pages
render to a buffer first, so a mid-template error never leaves a half-written
200 on the wire. `assetServer` hashes each embedded file once at startup and
serves it at `?v=<hash>` with a one-year immutable policy — correct only
because the files are embedded and cannot change while the process runs.

`render` sets `Cache-Control: no-cache` as the default and leaves a stronger
policy alone, so the strict cases stay opt-in and visible at the call site.

## Conventions

- **Times are UTC everywhere** — stored, served, and logged. The browser
  formats them for display, and the admin form posts its offset in minutes
  alongside the `datetime-local` value. This keeps `tzdata` out of the image,
  which is what lets the final Docker stage be `scratch`.
- **The image is `FROM scratch`.** No shell, no CA certificates, no tzdata.
  The healthcheck is therefore a subcommand of the binary itself
  (`teenyurl healthcheck`). Anything that needs an outbound TLS call or a
  timezone database breaks the image, not just the code.
- **Comments explain why, not what.** The existing ones give the rejected
  alternative or the failure mode. Match that; do not add comments that restate
  the line below.
- **Adding a dependency is a design decision.** There is one, and it is
  justified in DESIGN-DECISIONS.md. Use the standard library.

## Test conventions

`testServer(t)` in `routes_test.go` builds a server over `t.TempDir()` with the
clock pinned to `testTime` (`store_test.go`) and the password set to
`testPassword` (`main_test.go`). Both `s.now` and `s.auth.now` are pinned, so
expiry and session TTL are decided rather than raced.

For admin tests, `signIn(t, s)` returns the session cookie and a form token
scraped from the rendered page — the same two things a browser gets. Pass both
to `post(t, s, target, form, cookie, csrf)`; a request without either is
expected to be rejected.

Tests exercise handlers through `s.routes()` rather than calling them directly,
so routing precedence and the `guard` middleware stay covered.
