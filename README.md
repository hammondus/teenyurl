# teenyurl

A self-hosted URL shortener. One Go binary, no database, no third-party
JavaScript, and one dependency.

Short links point wherever you say, and you can change where they point later.
That is the reason to run your own rather than use a public shortener: a link
you printed, emailed, or turned into a QR code stays valid when the
destination moves.

## What it does

- Redirects a short code to a URL, and counts the visits.
- Lets you change a link's destination without minting a new code.
- Generates a QR code for any link, as SVG for print or PNG for the screen.
- Shows a preview page, so a recipient can see where a link goes before
  following it. Add a `+` to any short link.
- Expires a link at a time you choose.

Only you create links. There is no public form, no registration, and no user
accounts — one admin password guards the whole write surface.

## What it does not do

No per-click event log, no geographic reporting, no tags, no multi-user
accounts, and no link checking. See
[DESIGN-DECISIONS.md](DESIGN-DECISIONS.md) for why.

## Run it

You need Docker, a domain pointed at your host, and a reverse proxy in front.

1.  Clone the repository onto the server.

    ```
    git clone https://github.com/hammondus/teenyurl
    cd teenyurl
    ```

2.  Create the data directory and give it to the id the container runs as.
    The image runs as `nobody`, so a root-owned directory leaves the service
    unable to write.

    ```
    sudo mkdir -p /srv/teenyurl/data
    sudo chown 65534:65534 /srv/teenyurl/data
    ```

3.  Write the configuration. `.env` is git-ignored.

    ```
    cp .env.example .env
    ```

    Set `TEENYURL_BASE_URL` to your public address and
    `TEENYURL_ADMIN_PASSWORD` to something at least 12 characters long. The
    service refuses to start without one.

4.  Start it.

    ```
    docker compose up -d --build
    ```

The compose file joins an existing external network named `blobbyboo` and
publishes no ports. The proxy reaches `teenyurl:8080` over that network, so
the service has no host-facing socket and nothing can bypass the proxy,
`/admin` included. Change the network name if yours differs.

## Behind Nginx Proxy Manager

Add a proxy host:

- **Domain**: your short domain, for example `url.example.com`.
- **Forward hostname**: `teenyurl`, **port** `8080`, scheme `http`.
- **Websockets**: off.
- **SSL**: request a certificate and turn on **Force SSL** and **HTTP/2**.

Proxy Manager must be on the `blobbyboo` network for the hostname to resolve.

`TEENYURL_TRUSTED_PROXIES` decides which peers may set `X-Forwarded-For`. The
built-in default is loopback only, which is right for running the binary
directly and wrong behind Compose. `.env.example` sets the bridge subnet
instead. To find yours, run:

```
docker network inspect blobbyboo --format '{{range .IPAM.Config}}{{.Subnet}}{{end}}'
```

Getting it right matters in both directions. Set it too wide and a container
that can already reach `teenyurl:8080` forges the header and resets its own
login rate limit. Set it too narrow and every request is attributed to the
proxy, so five wrong passwords from anyone lock everyone out for fifteen
minutes.

## Configuration

Every setting is an environment variable.

| Variable | Default | Meaning |
| --- | --- | --- |
| `TEENYURL_BASE_URL` | `http://localhost:8080` | Public address. Short links and QR codes are built from it. An `https` value marks the session cookie `Secure`. |
| `TEENYURL_ADMIN_PASSWORD` | none | Required, at least 12 characters. |
| `TEENYURL_ADDR` | `:8080` | Listen address. |
| `TEENYURL_DATA_DIR` | `data` | Where the two data files live. |
| `TEENYURL_TRUSTED_PROXIES` | `127.0.0.1/32,::1/128` | Comma-separated CIDR blocks allowed to set forwarding headers. |
| `TEENYURL_SESSION_TTL` | `24h` | How long a sign-in lasts. |
| `TEENYURL_FLUSH_INTERVAL` | `30s` | How often click counts reach the disk. |
| `TEENYURL_CODE_LEN` | `6` | Characters in a generated code. |

## Using it

Sign in at `https://your.domain/admin`.

To create a link, paste a destination and press **Create link**. Leave the code
box empty for a random one, or type a memorable alias such as `docs`.

Each link's panel holds its QR code and an **Edit** panel. The delete control
lives inside that edit panel, so removing a link takes two deliberate clicks.

To see where a link goes without following it, add a `+`:
`https://your.domain/docs+`.

## Data and backups

Two files in the data directory:

- `links.jsonl` — one JSON object per line, append-only. The last record for a
  code wins, and a record with `"deleted": true` is a tombstone.
- `clicks.json` — a snapshot of the visit counts, rewritten on a timer.

Both are safe to copy while the service runs, so `rsync` needs no downtime and
no stop:

```
rsync -a /srv/teenyurl/data/ backup-host:/backups/teenyurl/
```

`links.jsonl` only grows at the end, so a copy taken mid-write is the file
minus its tail, which startup already tolerates. `clicks.json` is written
through a temporary file and a rename, so a reader gets either the old version
or the new one.

The format is also the import and export format. To bulk load links, append
lines and restart.

## Build from source

Go 1.26 or later. There is one dependency, `rsc.io/qr`.

```
make test      # go vet and go test -race
make build     # build for this machine
make release   # static linux/arm64 binary in dist/
make run       # run locally against ./data, reading .env
```

## Dependency

[`rsc.io/qr`](https://pkg.go.dev/rsc.io/qr) draws the QR codes. It pulls in no
third-party packages of its own, and it is small enough to read. Everything
else — HTTP, templating, JSON, image encoding, cryptography — is the Go
standard library.

## Design

[DESIGN-DECISIONS.md](DESIGN-DECISIONS.md) records the choices that are not
obvious from the code, and the alternatives that were rejected.
