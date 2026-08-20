# Design decisions — teenyurl

Rationale for choices that aren't obvious from the code, and the alternatives
that were rejected. Written before the first line of code and kept current as
the code landed.

A self-hosted URL shortener on `url.hammond.zone`. Single Go binary, embedded
vanilla frontend, Docker container behind Nginx Proxy Manager.

Module path and public repository: `github.com/hammondus/teenyurl`. The working
directory is `~/dev/short-url`; only the module path matters to Go.

The repository is public, so nothing under version control may hold a secret.
The admin password lives in a git-ignored `.env` on the server.

## Scope

Admin-only link creation, redirects, click counts, and QR codes. No user
accounts, no per-click event log, no tags, no link checking.

## Single binary, embedded frontend

The HTML, CSS, and JavaScript live in `web/` and compile into the binary
through `embed.FS`, matching `yourInfo`. Deployment is one file plus a data
directory.

Package layout is flat `package main`. The whole service is around 1,800 lines,
and splitting it across internal packages would add import ceremony without
making any boundary clearer.

## Storage: append-only log plus in-memory map

No database. Every link lives in a `map[string]*Link` in memory, and every
create, edit, or delete appends one JSON line to `links.jsonl`, followed by
`fsync`. Startup replays the file to rebuild the map.

Hundreds of links is a few hundred kilobytes of memory and a startup measured
in milliseconds. A database — even an embedded one — would add a large
dependency to solve a problem this service does not have.

### Each record is complete state, not a delta

A line holds the whole current value of a link. Replay is "last line wins per
code", which cannot drift. A delta log would need a correct replay rule per
operation type, and a bug in any one of them corrupts the map silently at the
next restart.

Deletion appends a record with `"deleted": true`. Without that tombstone,
replay resurrects every deleted link, because the create line is still there.

### Record tags use omitzero, not omitempty

`omitempty` never skips a struct, however empty, so a zero `time.Time` would be
written into every create record. `UpdatedAt` therefore uses `omitzero`, which
asks the value itself. The tag choice matters more here than in an API response
because the log is the durable format: junk written today has to be tolerated
by every replay from now on.

### The on-disk format is the import and export format

Bulk import is `cat more.jsonl >> links.jsonl` and a restart. Export is `cat`.
This falls out of the format rather than needing code.

### Clicks are separate from links

Clicks are frequent; link edits are rare. Two consequences:

- Clicks live in `map[string]*atomic.Int64`, parallel to the link map, so a
  redirect takes a read lock and an atomic increment. Concurrent redirects
  never block each other and never touch the disk.
- Counters are last-write-wins state, not events, so appending them to a log
  would be pure garbage growth. A goroutine rewrites `clicks.json` whole every
  30 seconds through a temp file and `rename`.

The cost is that an unclean kill loses up to 30 seconds of click counts. For a
vanity metric that beats an `fsync` on every redirect. It would be the wrong
trade for anything billed on.

### Crash tolerance

If the process dies mid-append, the last line of `links.jsonl` is truncated.
Replay skips a trailing line that fails to parse. A parse failure anywhere
other than the last line is a hard startup error, because that is corruption
rather than a crash.

### Compaction

At startup, if the file holds more than twice as many records as there are live
links, rewrite it through temp file and `rename`.

### Backups need no downtime

The data directory is a bind mount from `/srv/short-url/data` on the host, not
a named Docker volume, so `rsync` can target it directly. A named volume lives
under `/var/lib/docker/volumes/`, which is root-owned and awkward to address.
Copying while the service runs is safe:

- `links.jsonl` only ever grows at the end, so a copy taken mid-append is the
  file minus its tail, and replay already tolerates a truncated final line.
- `clicks.json` and compaction write through temp file plus `rename`. A reader
  that already opened the file keeps reading the complete old version, because
  `rename` does not disturb an open file descriptor.

## Auth: one password, no hash

`TEENYURL_ADMIN_PASSWORD` holds the plaintext, compared with
`subtle.ConstantTimeCompare`.

Hashing protects a password database from whoever reads it. There is no
database here. The credential sits in the server's `.env` file, and anyone who
can read a stored hash can equally read the container's environment. Argon2
would add `golang.org/x/crypto` to protect nothing. The real defences are the
login rate limit, HTTPS, and not reusing the password.

Because `make deploy` runs `git pull`, `docker-compose.yaml` is in the repo.
The password lives in a git-ignored `.env` on the server, loaded through
`env_file:`.

The service refuses to start when `TEENYURL_ADMIN_PASSWORD` is unset or under
twelve characters. Without that check a misconfigured deployment would serve an
admin surface that accepts an empty password, and the length is the only
strength the credential has.

Sessions are 32 random bytes in an in-memory map with a TTL. Restarting the
container logs you out. The cookie is `HttpOnly`, `Secure`, and `SameSite=Lax`.

`SameSite=Lax` is the primary cross-site request forgery defence: it stops
another site's form posting to `/admin`. A per-session token in a hidden field
backs it up.

Login is rate limited per client IP, five attempts per fifteen minutes.
Resolving the client IP correctly matters here, not just for logging:
`X-Forwarded-For` is attacker-controlled, so an attacker who can forge it
resets their own rate limit bucket. `clientip.go` honours forwarding headers
only when the immediate peer is in `TEENYURL_TRUSTED_PROXIES`, the same approach
as `yourInfo`. This is the second project to need that logic; a third makes it
worth extracting into a shared module.

## Admin UI

The delete control sits inside the per-row `<details>` edit panel rather than
beside each row. Destroying a link then takes two deliberate clicks using only
native HTML. The `<dialog>` confirmation that `admin.js` adds is an
enhancement on top, not the only guard, so the page stays safe when a script
fails to load. A bare delete button in every row, guarded only by JavaScript,
gets this backwards.

Everything else on the page follows the same rule. The forms post, the edit
panels open, and the filter box stays hidden until its script wires it up.

### Times are entered locally and stored as UTC

A `datetime-local` input carries no time zone. The page posts the browser's
offset in a hidden field, using the same sign as `Date.getTimezoneOffset`:
minutes to add to local time to reach UTC. With no offset, which means the page
ran without JavaScript, the server reads the value as UTC.

Times are rendered as UTC in `<time>` elements and rewritten into the reader's
zone by `admin.js`. That is what lets the container image skip `tzdata`.

### Confirmation text is not taken from the URL

After a create, edit, or delete the handler redirects to `/admin?created=<code>`
and so on. The wording is chosen from a fixed list in the handler, and only the
code comes from the query. A crafted link cannot put arbitrary text on the
page.

## Codes: 6 random characters, no lookalikes

The alphabet is 57 characters — base62 minus `0`, `O`, `I`, `l`, and `1`.
QR codes mean links get read off paper, and a link read off paper gets typed
by hand. Dropping the ambiguous glyphs costs 4 characters of alphabet and
removes the most common transcription error.

Six characters gives 34 billion combinations. Against hundreds of links, a
random guess lands about once in 10^8 tries. `TEENYURL_CODE_LEN` overrides it.

Bytes come from `crypto/rand` with rejection sampling: discard any byte of 228
or more, then take modulo 57. Plain `b % 57` biases the first 28 characters of
the alphabet, because 256 is not a multiple of 57.

Sequential codes were rejected. They are shorter, but they let anyone walk the
entire link set by counting.

Custom aliases match `^[A-Za-z0-9_-]{1,64}$` and are checked against a reserved
set: `admin`, `api`, `static`, `healthz`, `robots.txt`, `favicon.ico`.

Lookups are exact and case-sensitive.

## Redirects: 302, never 301

A `301` tells browsers and proxies to remember the destination permanently, so
the second click never reaches the server. That breaks the click counter and
makes "edit target URL" useless for anyone who already followed the link —
which is the main reason to self-host a shortener rather than use a public one.

Every redirect also carries `Cache-Control: no-store`, because some browsers
cache a `302` heuristically without one.

`307` was considered and rejected. It preserves the request method and body,
which is irrelevant to a link followed by GET from an address bar, and `302` is
what every shortener uses.

## The server never fetches a destination URL

No page title lookup, no favicon fetch, no link checker. The moment the server
fetches a user-supplied URL, it becomes a server-side request forgery tool
pointed at everything else on the Docker network.

Validation parses with `net/url` and requires a scheme of `http` or `https`
plus a non-empty host. That rejects `javascript:` and `data:`, which matters
because the preview page renders the destination inside an `<a href>` — an
unvalidated `javascript:` target would be stored cross-site scripting.

## Open redirect is the product

A URL shortener is an open redirect by definition. Destination filtering is
unwinnable and is not attempted. The mitigation is that only an authenticated
admin creates links. An unauthenticated shortener on a public domain becomes a
phishing relay within days, and then the domain lands on blocklists.

## Routes

| Route | Behaviour |
|---|---|
| `GET /{code}` | Increment counter, `302`, `Cache-Control: no-store` |
| `GET /{code}` expired | `410 Gone` |
| `GET /{code}` unknown | `404` |
| `GET /{code}+` | Preview page; does not count as a click |
| `GET /` | Landing page |
| `GET /robots.txt` | Allows `/` only |
| `GET /healthz` | `200 ok` for the Docker healthcheck |
| `/admin`, `/admin/*` | Session required |

Routing uses the Go 1.22 `ServeMux` wildcard. In a URL path a `+` is a literal
plus, unlike in a query string, so `r.PathValue("code")` returns `docs+` and
the handler strips the suffix.

The root serves a static landing page that says what the domain is and links to
`github.com/hammondus/teenyurl`. It carries no shortening form, because only an
authenticated admin creates links.

`robots.txt` allows the landing page and disallows everything else:

```
User-agent: *
Allow: /$
Disallow: /
```

The `Disallow` matters more than the `Allow`. Without it a crawler that finds a
short link anywhere follows it, and the code lands in a search index — which
turns an unguessable link into a public one.

The preview page sends `Referrer-Policy: no-referrer` and marks the continue
link `rel="noopener noreferrer"`, so the destination never learns which short
link sent the visitor.

## QR codes: rsc.io/qr

The only third-party dependency. The standard library has nothing, and a
hand-written encoder is 500 to 700 lines covering Reed-Solomon over GF(256),
data interleaving, mask pattern selection and scoring, and format and version
information — none of which is verifiable without a decoder to test against.

`rsc.io/qr` imports `rsc.io/qr/coding` and seven standard library packages, and
nothing else. Two files at the top level, small enough to read. BSD-3-Clause.
Last published 2018, which reflects a specification that does not move.

SVG output is about 30 lines written over `Code.Black(x, y)`.

Served at `/admin/qr/{code}.png` and `.svg`, admin-only, with a size cap.

## Configuration through environment variables

`yourInfo` uses flags. This service only ever runs in Compose, where
`environment:` and `env_file:` are the natural home for a password. Supporting
both would mean two places to look when a setting appears wrong.

```
TEENYURL_ADDR=:8080
TEENYURL_BASE_URL=https://url.hammond.zone
TEENYURL_DATA_DIR=/data
TEENYURL_ADMIN_PASSWORD=          # .env on the server, never committed
TEENYURL_TRUSTED_PROXIES=172.16.0.0/12,127.0.0.1/32,::1/128
TEENYURL_SESSION_TTL=24h
TEENYURL_FLUSH_INTERVAL=30s
TEENYURL_CODE_LEN=6
```

The prefix matches the binary name. It was `SHORT_` in the first draft of this
document, before the project had a name.

## Times are UTC everywhere

Stored, served, and logged as UTC. The browser formats them for display. This
keeps `tzdata` out of the image, which is what lets the final stage be
`scratch`.

## The healthcheck is a subcommand

`teenyurl healthcheck` asks the running server for `/healthz` and exits 0 or 1.
The final image is `FROM scratch`, so there is no shell and no `curl` for a
`HEALTHCHECK CMD` to run. The binary has to check itself.

## Container publishes no ports

The container joins the existing external `blobbyboo` network, where Nginx
Proxy Manager reaches `short-url:8080` directly. Omitting `ports:` means the
service has no host-facing socket, so nothing can bypass the proxy — including
`/admin`.

The final image is `FROM scratch` with `USER 65534:65534`. No CA certificates,
because the server makes no outbound calls. No `tzdata`, per above.

## Cache headers

Per house policy:

- Admin HTML: `no-cache, private`.
- Public HTML — preview, `404`, `410`: `no-cache`.
- Redirects: `no-store`.
- Static assets: `?v=<hash>` with `public, max-age=31536000, immutable`.

Hashing assets once at startup is correct here specifically because they are
embedded in the binary and cannot change while the process runs.

## QR images are drawn here, not by the library

Only `qr.Encode` and `Code.Black` come from `rsc.io/qr`. Both the PNG and the
SVG are drawn in `qr.go`, so the quiet zone and the pixel size are identical in
each form rather than whatever the library's own PNG writer happens to do. The
PNG is a two-colour paletted image, which keeps it small.

The QR encodes the short link, never the destination. A printed code that
carried the destination would bypass the shortener, and the link would stop
being editable — which is the main reason to self-host one.

Two tests guard the rendering rather than the library: one compares every drawn
pixel against `Code.Black` for that module, which catches a transposed x and y
or a misplaced quiet zone, and one checks the three finder patterns a scanner
locks onto.

## Deliberately out of scope

Multi-user accounts, per-click event logs and geographic reporting, tags and
folders, per-link custom domains, and link health checking. The note field plus
client-side filtering covers most of what tags would do.

Case-insensitive fallback on a `404` was considered for links typed off paper
and rejected: it can be ambiguous, and the lookalike-free alphabet already
removes the common failure.
