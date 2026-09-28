# Journal

A private journal and reading room for a small, invited circle. Members write here and read one merged feed: posts from this site plus their friends' posts on LiveJournal, Dreamwidth, lj.rossia.org, and any RSS or Atom feed. Remote services are read with each member's own credentials or public feeds and are never written to.

The design, threat model, and decisions are in [SPEC.md](SPEC.md).

## Scale

Built for about 5–30 people on one small host: one process, one SQLite file, 1 vCPU and 512 MB RAM. It does not scale out and makes no claim to serve hundreds or thousands of users. [SPEC.md §3](SPEC.md#3-scale-envelope) says what would change with growth.

## Try it

The demo runs against recorded fixtures, with no accounts on any service and no network access to them:

```sh
go run ./cmd/journal demo
```

It listens on `http://127.0.0.1:8080` only and prints the demo handles and passphrases. The demo database is temporary.

## Configure

Generate a 32-byte key:

```sh
openssl rand -base64 32
```

On Windows PowerShell:

```powershell
[Convert]::ToBase64String((1..32 | ForEach-Object { Get-Random -Maximum 256 }))
```

Copy `.env.example` to `.env` and set at least these:

| Variable | Purpose |
| --- | --- |
| `SECRET_KEY` | root key for cookie signing, URL signing, and token encryption |
| `BASE_URL` | public `https://` origin |
| `SITE_ADDRESS` | DNS name Caddy uses for TLS |
| `OPERATOR_CONTACT` | your email or URL, sent in the User-Agent to the services this site reads |

Everything else has a default; the full list is in [SPEC.md §13.1](SPEC.md#131-configuration).

## Deploy

Point DNS for `SITE_ADDRESS` at the host, then:

```sh
docker compose up -d --build
docker compose exec journal journal invite --admin
```

Open the printed link to create the first admin profile. Admins invite everyone else from the admin page. Membership stops at `MAX_USERS`.

Members sign in with a handle and passphrase, then link their service accounts in Settings. Service passwords are stored only in a vault encrypted with the member's passphrase. A forgotten passphrase cannot be recovered; an admin can issue a recovery link, and the member then re-enters their service passwords.

To run without Compose:

```sh
go build -o journal ./cmd/journal
./journal
```

## Backups and upgrades

- A backup is taken daily into `data/backups`, keeping 7 daily and 4 weekly copies. Run one now with `journal backup`.
- Copy backups off the host yourself. For example, run `restic backup /var/lib/docker/volumes/<project>_journal-data/_data/backups` from cron.
- To restore, stop the server first:

  ```sh
  docker compose stop journal
  docker compose run --rm journal journal restore /data/backups/<file>
  docker compose start journal
  ```

- Upgrading is `git pull` and `docker compose up -d --build`. Before migrating, the server backs up the database; if a migration fails, it refuses to start and names the backup to restore.
- To rotate `SECRET_KEY`: move the current key to `SECRET_KEY_PREVIOUS`, set a new `SECRET_KEY`, restart, and run `journal rotate-keys`. Remove `SECRET_KEY_PREVIOUS` 30 days later.

## Operations

- `/healthz` and `/readyz` are the health endpoints. Compose uses `/healthz`.
- Logs are JSON on stdout, one line per request and per sync job.
- Prometheus metrics are served on `127.0.0.1:9091`, inside the container only.
- The admin page shows each account's and feed's sync status, paused hosts, invites, and legacy secrets.

## Sync

- Each linked account is read every 20 minutes (±5). Each run refreshes the newest friends page and continues a walk through the last 14 days, a few pages at a time.
- Followed public journals are read hourly, with conditional requests.
- Requests to any one service are spaced at least a second apart.
- A captcha, block, or `Retry-After` pauses that whole service for at least 6 hours.
- Posts a source stops showing to a member disappear here after the next full walk. Remote content expires after 30 days (friends-locked) or 90 days (public).

## Probe

`cmd/ljprobe` checks a LiveJournal-code service one request at a time and writes a report to `probe-out/`:

```sh
go run ./cmd/ljprobe -service livejournal
go run ./cmd/ljprobe -service dreamwidth
go run ./cmd/ljprobe -service rossia
```

Linking accounts on a service is allowed only for services listed in `CREDENTIALED_SERVICES` (default `livejournal`); add a service there after its probe passes. With `LJ_USER` and `LJ_PASSWORD` set, the probe also tests the credentialed methods and writes the session cookie to `probe-out/ljsession.txt`. That directory, `.env`, and the database are gitignored and contain secrets. Share this project only through git, never as a copy of the working folder.

## Development

One command runs the full check suite: vet, staticcheck, govulncheck, tests (with the race detector where cgo is available), short fuzz runs, the performance test, browser tests, and the container smoke test when Docker is available.

```sh
go run ./tools/check
```

`go run ./tools/check -quick` skips the browser, performance, fuzz, and container stages. CI runs the full suite on every push.

## How this was built

Built with AI coding agents working from [SPEC.md](SPEC.md). The problem framing, design, threat model, and acceptance criteria are the author's. Each change lands as its own commit with its tests, and CI checks every commit.
