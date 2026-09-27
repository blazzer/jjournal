# Journal

A private, server-rendered journal for a small circle. People log in with the username and password they already use on LiveJournal. The site reads their friends page in the background and merges it with posts written here. It never posts or comments back.

The site name comes from `SITE_NAME` and defaults to Journal.

## Configure

Generate a 32-byte key:

```sh
openssl rand -base64 32
```

On Windows PowerShell:

```powershell
[Convert]::ToBase64String((1..32 | ForEach-Object { Get-Random -Maximum 256 }))
```

Copy `.env.example` to `.env` and set:

| Variable | Purpose |
| --- | --- |
| `SECRET_KEY` | 32-byte base64 key for AES-GCM and cookie signing |
| `BASE_URL` | Public `https://` origin |
| `SITE_NAME` | Name in the header. Default `Journal` |
| `SITE_ADDRESS` | DNS name Caddy uses for TLS |
| `LJ_SOURCE` | `xmlrpc` (default), `digest`, or `scrape` |
| `DB_PATH` | SQLite file. In Compose this is `/data/journal.db` |
| `LISTEN_ADDR` | Bind address. Default `:8080` |

`LJ_USER` and `LJ_PASSWORD` are only for the optional `ljprobe` tool. The server does not use them. Passwords are hashed immediately and stored only as an encrypted MD5. Plaintext is never written to disk or logs.

Reading order is the source you set, then `xmlrpc`, then `digest`, then `scrape`.

## Sync

Each account is read about every 20 minutes. LiveJournal keeps at most 1,000 friends-page entries from the last two weeks, and `getfriendspage` returns 50 at a time. A sync refreshes the newest page and walks a few older pages, then resumes there next time. Posts are stored as they are fetched, and a later fetch updates the same post. Calls are spaced at least one second apart, with at most two in flight. A captcha or HTTP 403/429 pauses that account for six hours.

## Deploy

Point DNS for `SITE_ADDRESS` at this host, then:

```sh
docker compose up -d --build
```

Caddy obtains a certificate and proxies to the app. The first account to log in is the admin and can open Sync to see each person's status. Later accounts are not admins.

Data lives in the `journal-data` volume: the database, cached userpics, and proxied images.

To run on the host instead of Compose, set the variables above and:

```sh
go build -o journal ./cmd/journal
./journal
```

Use an HTTPS `BASE_URL` when a proxy terminates TLS. The session cookie is `Secure` in that case.

## Probe

`cmd/ljprobe` checks XML-RPC, digest RSS, and the HTML friends page one request at a time. Without credentials it writes `probe-out/report.txt` and exits 0:

```sh
go run ./cmd/ljprobe
```

With `LJ_USER` and `LJ_PASSWORD` set, it writes the same report plus `probe-out/ljsession.txt`. That directory is gitignored. The tool does not print the password or the session cookie.
