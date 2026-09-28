# Journal: design

A private journal and reading room for a small, invited circle. Members write posts here and read, in one feed, what their friends post here and on other journal services. Remote services are read through each member's own credentials or public feeds, and are never written to.

## 1. Problem and users

A group of about 5–30 people keeps journals across LiveJournal, Dreamwidth, lj.rossia.org and similar sites, and some want to move their writing somewhere they control. They want:

- one reading page that merges friends-locked and public posts from those services with posts written here;
- a place to write, with friends-only and group-restricted posts and threaded comments;
- no loss of privacy: nobody sees a remote post that its source would not show them.

The reading page on each source service already works; nobody wants to replace those services or re-host their content publicly.

## 2. Goals, non-goals, success criteria

Goals:

- Merged reading page across services and native posts, with per-viewer visibility that matches each source.
- Native journaling with LiveJournal-style security (public, friends, private, custom groups) and threaded comments.
- Members control their reading list: follow public journals by handle, hide people, without changing anything on the source service.
- Credentials for remote services are protected so that a copy of the database, even with the server key, does not reveal passwords.
- One small process that one person can operate.

Non-goals: scaling beyond the envelope in section 3, multiple instances, high availability, posting or commenting to remote services, Facebook or Threads feeds, native communities, full-text search, notifications, email, custom per-journal layouts, imitating any service's branding.

Success criteria, each backed by an automated check (section 14):

| Criterion | Target | Check |
| --- | --- | --- |
| Feed freshness | new friends-page posts visible within 30 minutes for healthy accounts, p95 | scheduler test with fake clock |
| Reading page latency | server time p95 under 150 ms with 30 members and 100,000 stored entries | perf test |
| Memory | runs within a 512 MB container limit | container smoke test |
| Secrets | no plaintext password, passphrase, password hash or token in logs, responses, or the database file | secret-scan test |
| Politeness | never more than 1 request per second to a service API host; no request to a paused host | pacing test with fake clock |
| Revocation | a remote post the source stops showing to a member disappears for that member within one full friends-page walk, and every stored copy is gone within the retention period | lifecycle test |
| Recoverability | a backup restores to a working database | backup round-trip test |

## 3. Scale envelope

This is a deliberately small system. It makes no claim to serve hundreds or thousands of users.

- One process, one container, one SQLite file on a local volume. No replicas, no external queue, cache, or database.
- Target footprint: 1 vCPU, 512 MB RAM, a few GB of disk including caches.
- Target load: up to `MAX_USERS` members (default 30), a few hundred followed remote journals, low single-digit requests per second.
- In-memory state (login rate limits, the unlocked-vault cache, the argon2 queue) is acceptable because there is exactly one process. A restart clears it. Host pauses and sync state are persisted.

What changes with growth:

| Scale | First constraint to break | What changes |
| --- | --- | --- |
| 10× (300 members) | Third-party request budget, not this server: 300 accounts polling every 20 minutes approaches 1 request per second per service | Budget-aware scheduler that stretches intervals per host; share one friends-page fetch where members overlap; public feeds by conditional GET only |
| 100× (3,000 members) | Remote services will block the source IP; single-writer SQLite and in-process scheduler | Official API agreements or dropping credentialed sync; Postgres; job queue; shared session and rate-limit store; object storage for images; several stateless web processes |
| 1,000× | The product premise (a private reading room holding members' credentials) | A different product |

## 4. Architecture

### 4.1 Hub and spoke

```
            +---------------------------- hub -----------------------------+
 browser -> | web: shared HTTP (sessions, CSRF, headers, rate limits)      |
            |   front-end: classic          front-end: modern              |
            |   (controllers + templates)   (controllers + templates, JS)  |
            |--------------------------------------------------------------|
            | app: use cases, returns view models (no HTTP, no HTML)       |
            | identity + vault      feed assembly      render (sanitize)   |
            | store (SQLite)        visibility + GC    image/userpic cache |
            | scheduler (accounts, feeds)   outbound (pacing, SSRF guard)  |
            +----------+-------------------+-------------------+----------+
                       |                   |                   |
             spoke: livejournal   spoke: dreamwidth   spoke: rossia   spoke: feed
```

- The hub owns identity, storage, visibility, suppression, rendering, scheduling, outbound HTTP, and UI. It contains no service-specific hosts or protocol code.
- A spoke implements the `Service` interface. It receives an outbound HTTP client from the hub and never makes its own. Spokes never import `store` or `app` and never see a passphrase.
- Spokes register in a registry keyed by service ID. Adding a service means adding a spoke and registering it.
- LiveJournal-code sites share one protocol implementation, the `lj` package, configured per host.

### 4.2 Service interface

```go
type Service interface {
    ID() string          // "livejournal", "dreamwidth", "rossia", "feed"
    Name() string        // shown in the UI
    Caps() Caps          // which methods below work

    // Credentialed reading (Caps.Login).
    Login(ctx context.Context, user string, secret Secret) (Session, error)
    FriendList(ctx context.Context, s Session) ([]RemoteFriend, []RemoteGroup, error)
    FriendsPage(ctx context.Context, s Session, skip int) (FriendsPage, error)

    // Anonymous reading of one journal (Caps.PublicFeed).
    PublicFeed(ctx context.Context, journal string, cond Conditional) (FeedResult, error)

    // Handle and URL mapping.
    NormalizeHandle(input string) (journal string, err error)
    ParseURL(u *url.URL) (journal string, ok bool)
    JournalURL(journal string) string
}
```

- `Secret` for LiveJournal-code sites is the hex MD5 of the password, which their challenge-response login requires.
- `Session` carries a reusable token (for example `ljsession`).
- `FriendsPage` returns entries plus the window facts the hub needs for revocation: whether the page reached the end of what the service will return.
- Errors are typed: auth, blocked (HTTP 403/429 or captcha), unsupported, fault. A `Retry-After` value travels with blocked errors.

### 4.3 Services

| ID | Host | Credentialed reading | Public feed | Handle |
| --- | --- | --- | --- | --- |
| `livejournal` | livejournal.com | XML-RPC with digest and scrape fallbacks | `/data/atom` | username |
| `dreamwidth` | dreamwidth.org | XML-RPC, if the probe confirms `getfriendspage` | `/data/atom` | username |
| `rossia` | lj.rossia.org | XML-RPC, if the probe confirms `getfriendspage` | `/users/<name>/data/rss` | username |
| `feed` | any | none | the URL itself (RSS or Atom) | feed URL |

The `feed` spoke follows any RSS or Atom URL and exists to keep the hub honest: it is a service with a different protocol, identity scheme, and no login. `cmd/ljprobe -service <id>` checks a LiveJournal-code host. Credentialed reading is enabled per service by `CREDENTIALED_SERVICES` (default `livejournal`), so a service is added there only after the probe succeeds; public feeds work for every service regardless.

### 4.4 Front-ends: controller and model, not skins

The classic and modern UIs differ in more than styling. Classic is HTML only: full pages, post-redirect-get, confirmation pages. Modern composes pages differently, has card menus and dialogs, and updates parts of a page in place. The hub therefore uses a loose controller–model split:

- **Model, package `app`.** Use cases take a viewer and plain inputs and return view models (plain structs, with already-sanitized HTML where needed) or typed errors (`Invalid`, `NotFound`, `Forbidden`, `Conflict`, `RateLimited`). `app` owns validation, authorization, and business rules and imports neither `net/http` nor `html/template`.
- **Shared web layer, package `web`.** Sessions, CSRF, security headers, rate limits, static files, image routes, health, and dispatch to the viewer's front-end. Logged-out pages use modern.
- **Controllers, packages `web/classic` and `web/modern`.** Each has its own handlers and templates. Handlers parse the request, call `app`, and render. They contain no SQL and no business rules.
- **URLs.** Shareable pages have the same paths in both front-ends, so a link works whichever UI a member chose. Modern adds fragment endpoints under `/m/` that return HTML partials; each has a plain form route with the same effect.
- **Classic is frozen at parity.** It covers every core flow (read, write, comment, manage lists, settings, admin) with minimal templates, and receives no modern-only conveniences.

### 4.5 Composition

`cmd/journal` is the composition root. It reads config, opens the store (running migrations and the pre-migration backup), builds the outbound client, registry, scheduler, and web server, and owns shutdown order: stop accepting HTTP, drain HTTP (10 s), stop the scheduler and wait for in-flight jobs (30 s), stop the backup job, close the store.

Subcommands: `serve` (default), `invite`, `backup`, `restore`, `rotate-keys`, `demo`.

## 5. Membership and identity

- A member is a local profile: a handle (unique, lowercase, `[a-z0-9_]{2,30}`), display name, passphrase, and zero or more linked service accounts.
- Membership is by invite only. An invite is a single-use link that expires after 7 days. Admins create invites in the admin page; the operator creates the first admin invite with `journal invite --admin`. Signups stop at `MAX_USERS`.
- Admin is a flag on the profile, not a position in the table. There can be several admins.
- The signup page states that the operator of this site can technically read everything stored here, including mirrored friends-locked posts.

### 5.1 Login paths

| Path | Who | Result |
| --- | --- | --- |
| Handle + passphrase | any member | session |
| Service username + password | only a service account that is linked to no profile, which exists only for members who joined before profiles existed | forced profile creation, then session |
| Service username + password | a service account linked to a profile | refused: "Sign in with your handle and passphrase" |

A service password never grants a session to an existing profile. Linking a service account happens in settings, while signed in, and requires that service's password.

### 5.2 Recovery

A forgotten passphrase cannot be recovered; the vault is lost.

1. An admin issues a recovery link for the member (single-use, 24 hours). The admin page shows it once, to be handed over out of band.
2. If the profile has linked service accounts, the member must first sign in to one of them with its current password, which proves they control it. A profile with no linked accounts relies on the admin's judgment alone.
3. The member sets a new passphrase. The old vault, all stored service secrets, and all sessions are deleted. Posts, comments, friends, and lists are kept. Other service accounts show `needs_password` until the member re-enters them.

### 5.3 Account deletion

A member can delete their profile from settings after re-entering their passphrase; an admin can delete any profile. Deletion removes the profile, linked accounts, secrets, tokens, sessions, visibility grants, lists, groups, and native posts. Their comments on other members' posts become "[deleted]" so threads stay intact.

## 6. Security

### 6.1 Key hierarchy

- `SECRET_KEY` (32 bytes) is the root. Subkeys are derived with HKDF-SHA256 using fixed labels: session-cookie signing, CSRF, image-URL signing, and token encryption. The root key is never used directly.
- Each ciphertext and signature records the ID of the root key that produced it. `SECRET_KEY_PREVIOUS` is accepted for decryption and verification only. `journal rotate-keys` re-encrypts everything under the current key; after it runs and sessions older than 30 days have expired, the previous key can be removed.
- Per-member vaults: a random 32-byte data key (DEK) per profile, wrapped with AES-GCM under a key derived from the passphrase with argon2id. Salt (16 bytes) and parameters are stored per profile. Default parameters: 19 MiB memory, 2 iterations, parallelism 1.
- Service secrets are encrypted with the DEK. The additional authenticated data binds each ciphertext to its profile ID, account ID, and purpose, so ciphertexts cannot be swapped between rows.
- A passphrase is correct when the DEK unwraps. No separate passphrase hash is stored. Minimum length 12. Changing the passphrase re-wraps the DEK and ends all other sessions. Rotating `SECRET_KEY` does not touch vaults.

### 6.2 Tokens, secrets, and background sync

- Service session tokens are encrypted with the token subkey, so sync runs while members are signed out.
- Storing the service password (as its MD5) in the vault is a per-account choice, on by default, labelled "Remember password". That MD5 is unsalted and is what the service's login protocol needs; it is password-equivalent for that service and cheap to crack offline, so it exists only under the DEK.
- When a service rejects a token:
  - with a remembered password, the account moves to `needs_unlock`; at the member's next sign-in the hub unwraps the DEK, logs in to the service, stores the new token, and resumes sync;
  - without one, the account moves to `needs_password` and the member re-enters it in settings.
- After sign-in the unwrapped DEK stays in process memory, keyed to that web session, for 15 minutes, so immediate follow-ups (linking another account, retrying one) do not ask again. It is never written to disk and is dropped on sign-out, passphrase change, or restart.
- Members who joined before profiles existed have service secrets under the server key. At their next service-password sign-in they must create a profile, their secret moves into the vault, and the server-key copy is deleted. The admin page lists remaining legacy secrets and can purge them.

### 6.3 Sign-in abuse

- Failures are counted per (handle, client IP): 5 in 15 minutes pauses that pair for 15 minutes.
- Per client IP: 20 failures in 15 minutes pauses the IP.
- Per handle across all IPs: each failure adds a delay (1 s, doubling, capped at 30 s). Nobody can lock a member out; they can only slow attempts down.
- Unknown handles run a full argon2 derivation against a dummy vault, so timing does not reveal which handles exist.
- At most 2 derivations run at once. A request that waits more than 5 seconds for a slot gets HTTP 503 with `Retry-After`.
- Service-password attempts (legacy sign-in and linking) are limited to 5 failures per hour per profile or IP, so the site cannot be used to guess passwords for a service.
- Signups are limited per IP.

### 6.4 Web

- Session cookies: signed, `HttpOnly`, `SameSite=Lax`, `Secure` behind HTTPS, 30-day expiry, backed by revocable server-side rows.
- CSRF tokens on every POST, including requests from JavaScript (header or form field).
- `GET` never changes state.
- Headers on every response:
  - `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; frame-src` limited to the YouTube and Vimeo allowlist, plus `form-action 'self'; base-uri 'self'; frame-ancestors 'none'`;
  - `Referrer-Policy: same-origin`;
  - `X-Content-Type-Options: nosniff`.
- No inline script or inline event handlers.
- No credentials or long-lived secrets in URLs. Single-use invite and recovery tokens are the only exception: they are exchanged for a cookie on first use, and the browser is redirected to a clean URL.
- Remote HTML is sanitized when stored and again when rendered, with an allowlist close to LiveJournal's. Every image is served through the site's own signed proxy.

### 6.5 Outbound requests

All outbound HTTP goes through one hub client:

- **SSRF guard.** The client resolves each host, rejects loopback, private, link-local, CGNAT, documentation, and multicast addresses, and dials the vetted IP itself, so a second DNS answer cannot redirect it. A dial-time check verifies the connected address. Environment proxies are ignored. Each redirect is re-validated, with at most 3.
- **Identity.** `User-Agent: Journal/<version> (private reader; +<OPERATOR_CONTACT>)`. The client never presents itself as a browser.
- **Pacing.** At most 2 API requests in flight across the process, and at least 1 second between requests to the same API host. Image hosts are a separate lane: at most 4 in flight and at least 250 ms apart per host.
- **Response limits.** Size caps: 5 MB per image, 10 MB per API or feed response. Timeouts of 10 s to connect and 30 s in total.
- **Pauses.** `Retry-After` is honoured. HTTP 429, 403 with block markers, or a captcha page pauses that host, not only that account, for the longer of `Retry-After` or 6 hours. Host pauses are stored in the database and survive restarts.

### 6.6 Threat model

Assets: native posts and comments, mirrored friends-locked posts written by people who are not members, service passwords (MD5), service tokens, member passphrases, sessions.

| Actor | Can | Mitigation | Residual risk |
| --- | --- | --- | --- |
| Internet attacker, no account | reach login, signup, static files | invite-only signup, rate limits, CSRF, CSP, SSRF guard | none known |
| Attacker holding a member's service password | try to sign in or take over | service password never grants a session to a profile | can read that service directly, which is outside this site |
| Another member | request any URL | authorization in `app` for every entry, comment, and list; tested as a matrix | none known |
| Thief of a database copy | offline access | secrets under DEKs need passphrases (argon2id); tokens and signatures need `SECRET_KEY` | native posts and mirrored posts are plaintext in the file |
| Thief of database and `SECRET_KEY` | decrypt tokens | tokens expire and are revocable at the service; passwords stay under DEKs | can read services through unexpired tokens |
| Operator with shell on the running host | read memory, database, and keys | disclosed at signup; admin UI never shows other members' content or secrets | full access; the design trusts the operator |
| Remote service, or hostile content in it | serve hostile HTML, images, redirects | double sanitizing, image proxy with type sniffing and size caps, SSRF guard | none known |
| Non-member author | expects their locked post to reach only their friends | visibility invariant, revocation, retention (section 7) | copies persist here until the next full walk, or the retention limit at most |

## 7. Third parties and privacy

### 7.1 Visibility invariant

A remote post is shown only to a member the source service would show it to now, within bounded delay. Concretely:

- A friends-locked or custom post is visible to a member only through a visibility grant created by that member's own credentialed sync.
- A public post is visible through a grant, or to members who follow its journal.
- Grants are revoked when the source stops returning the post (7.2) and expire otherwise (7.3).

### 7.2 Revocation

- A friends page is a window, not an archive: on LiveJournal-code sites, up to 1,000 entries from the last 14 days. Sync walks the window a few pages per run and records a walk generation.
- When a walk completes, grants for that account's member are removed for entries inside the covered time range that were not returned in that walk. That covers deleted posts, posts locked away from the member, and the member removed from a friends group. With default pacing a full walk takes under 3 hours.
- A credentialed copy is authoritative. When credentialed sync sees a post as locked, it becomes locked, and follower-based visibility stops because that rule requires `public`.
- A feed copy may create a post or update one that only a feed has ever supplied. It never overwrites a body or security level that came from credentialed sync, because feeds are often truncated and never show locked posts.

### 7.3 Retention

- A grant on a friends-locked or custom post expires `RETENTION_LOCKED_DAYS` (default 30) after the post was last returned by the source.
- A public post from a feed is kept `RETENTION_PUBLIC_DAYS` (default 90) after it was last seen in the feed.
- A daily cleanup job deletes expired grants, then remote entries that no member can see. Cached images age out under the cache size cap.
- Native posts are kept until their author deletes them or the profile.

### 7.4 Terms of service

Each service's terms and bot policy apply to the operator. The software is built to stay within them:

- it reads only what a member's own credentials or public feeds expose;
- it shows content only to members the source would show it to;
- it identifies itself honestly;
- it paces requests and stops on block signals;
- it never writes to a service.

Before enabling credentialed reading for a new service, the operator runs `ljprobe` against it and reviews that service's terms.

## 8. Data model

SQLite, schema changes by numbered SQL migrations plus idempotent Go data migrations. All timestamps are UTC text in the fixed-width format `YYYY-MM-DDTHH:MM:SS.sssZ`, so text order is time order.

- `users`: id, handle (unique), display_name, is_admin, ui (`classic` | `modern`), kdf_salt, kdf_params, dek_wrapped, migrated_at, created_at.
- `invites`: token_hash, created_by, is_admin, created_at, expires_at, used_by, used_at.
- `recovery_tokens`: token_hash, user_id, created_by, expires_at, used_at.
- `accounts`: id, user_id, service, remote_username, secret_enc, secret_scheme (`dek` | `legacy` | `none`), remember_secret, token_enc, key_id, sync_status (`ok` | `needs_unlock` | `needs_password` | `auth_failed` | `blocked` | `error`), sync_error, fail_count, last_synced_at, friends_synced_at, next_sync_at, walk_generation, walk_skip, walk_oldest_seen, created_at. Unique on (service, remote_username).
- `remote_friends`: account_id, friend_username, groupmask, synced_at.
- `native_friends`: user_id, friend_user_id, groupmask.
- `friend_groups`: id, user_id, account_id (null for native groups), name, bit.
- `subscriptions`: user_id, service, journal, created_at.
- `public_feeds`: service, journal, etag, last_modified, status, error, fail_count, last_fetched_at, next_fetch_at.
- `suppressions`: user_id, service, username, created_at.
- `host_pauses`: host, paused_until, reason.
- `entries`: id, service (`local` for native), source (`remote` | `native`), origin (`friendspage` | `feed` | `native`), author_username, journal_username, journal_type, remote_id, url, subject, body_html, security, allowmask, event_time, userpic_url, mood, music, remote_comment_count, last_seen_at, created_at, updated_at. Unique on (service, journal_username, remote_id) for remote entries.
- `entry_visibility`: entry_id, viewer_user_id, account_id, walk_generation, last_seen_at.
- `comments`: id, entry_id (native entries only), parent_id, author_user_id (null when the author deleted their profile), body_html, created_at, deleted.
- `sessions`: id, user_id, created_at, expires_at, last_seen_at.

## 9. Reading page

`/~{handle}/friends?skip=N`, 20 entries per page:

1. Remote entries visible to the viewer (7.1).
2. Native entries from the viewer's friends, under security and allowmask rules.
3. Drop remote entries by an account linked to a member whose `migrated_at` is set, when `event_time >= migrated_at`.
4. Drop suppressed entries (10.1), before paging, so pages stay full.
5. Apply `?filter=<group>`.
6. Order by `event_time` descending, then id descending.

The same post may arrive by friends page and by feed; the unique key keeps one row and visibility is the union of both paths.

## 10. Reading list and suppression

| Action | Target | Effect |
| --- | --- | --- |
| Add to reading list | handle + "This site" | add `native_friends` row |
| Add to reading list | handle + a remote service | add `subscriptions` row, create or reuse the `public_feeds` row, fetch now |
| Delete from reading list | member of this site | delete `native_friends` row |
| Delete from reading list | journal added by handle | delete `subscriptions` row |
| Delete from reading list | friend mirrored from a service | add `suppressions` row; the service is not changed |
| Add to suppressed list | handle + any service, including "This site" | add `suppressions` row |
| Delete from suppressed list | any row | delete `suppressions` row |

### 10.1 Rules

- A suppression matches an entry when its (service, username) equals the entry's author or the entry's journal. Suppressing a person hides their posts everywhere; suppressing a community hides the community.
- Anyone can be suppressed, including people not on the reading list.

### 10.2 Controls

Both lists live on `/manage/friends`. The suppressed list also appears on the owner's profile; other viewers do not see it.

- **Add form, the same for both lists:** a handle field and a service select listing "This site" first, then every registered service by name. If the handle field holds a URL that a service recognizes, that service is used.
- **Errors** (unknown member, invalid handle, duplicate) show next to the form. Remote journals are not checked when added; a failed first fetch shows on the row.
- **Rows** show the userpic when known, the handle, a service badge, and, on the reading list, the origin: here, added, or mirrored.
- **Delete:** every row has an icon button (trash icon, `aria-label="Delete {handle}"`) that always asks for confirmation:
  - Classic links to a confirmation page (`GET`, no state change) with a Delete form (`POST`) and a Cancel link.
  - Modern opens the same text in a `<dialog>`, submits the `POST`, and removes the row in place; without JavaScript it behaves like classic.
  - The text states the effect. For a mirrored friend: "{handle} is on your {service} friends list. Their posts will be hidden here; your {service} list is not changed."
- Modern entry cards also offer "Hide author", with the same confirmation.

## 11. Scheduling

- **Account sync:** every 20 minutes, ±5 minutes of random jitter. Each run refreshes the newest friends page and continues the window walk for up to 3 more pages. The friend list is refreshed at most daily.
- **Public feeds:** every 60 minutes, ±10 minutes of jitter, by conditional GET (ETag, Last-Modified). One fetch per feed, however many members follow it.
- **Backoff after errors:** the normal interval doubled per consecutive failure (20 min, 40 min, 80 min, …), capped at 6 hours, with ±20% jitter. A failure never shortens the next wait.
- **Blocks and `Retry-After`** pause the host (6.5). Auth errors are per account (6.2).
- **Kicks:** new subscriptions and newly linked accounts run immediately, still subject to pacing.
- **Images:** userpics and entry images are fetched in the background after sync, so page views rarely fetch remotely. A cache miss on view fetches through the image lane.

## 12. Content and UI

### 12.1 Content

- `<lj-cut>` on list pages becomes `<details class="cut"><summary>{cut text or "Read more"}</summary>{content}</details>`. The full text is stored, so it expands without JavaScript or a network call. Images inside cuts load lazily. The entry page shows the content unwrapped.
- `<lj user="x">` links to the local profile when x is a member linked on that service, otherwise to the entry's own service.
- Iframes are allowed only from YouTube and Vimeo.

### 12.2 UI

- Two front-ends (4.4). Members switch in settings. New profiles default to modern; members who joined before profiles existed keep classic until they switch.
- Modern is a current, polished reading app, not a copy of any service:
  - **Rendering:** server-rendered HTML first. JavaScript is progressive enhancement in plain ES modules from `/static`, no framework and no build step. There is no client-side router and no infinite scroll.
  - **Enhancements:** in-place list changes, `<dialog>` confirmations, a "Load more" button that appends the next page and updates the URL, inline comment threads on native entries, and j/k keyboard navigation.
  - **Layout:** a sticky top bar, a single-column feed of entry cards, a side panel on wide screens with profile, groups and linked services with sync status, and mobile-first sizing.
  - **CSS:** custom properties, container queries, `:has()`, view transitions where supported, `prefers-color-scheme`, and `prefers-reduced-motion`.
  - **Accessibility:** landmarks, visible focus, 4.5:1 contrast, and labelled controls.
- Every page and form in both front-ends works with JavaScript disabled.
- Original assets only.

### 12.3 Routes

| Path | Purpose |
| --- | --- |
| `/login`, `/logout` | sign in and out (5.1) |
| `/signup?invite=…` | create a profile from an invite |
| `/recover?token=…` | recovery (5.2) |
| `/~{handle}/`, `/~{handle}/friends`, `/~{handle}/{id}.html`, `/~{handle}/profile` | journal, reading page, entry, profile |
| `/update` | post editor |
| `/manage/friends`, `/manage/friends/delete` | lists and delete confirmation |
| `/settings` | display name, UI, passphrase, linked accounts, delete profile |
| `/admin` | members, invites, recovery, sync and feed status, host pauses, legacy secrets, key figures |
| `/img`, `/userpics/…` | cached images |
| `/healthz`, `/readyz` | liveness; readiness (database reachable, migrations current) |
| `/m/…` | modern-only fragments, each with a plain form equivalent |

## 13. Operations

### 13.1 Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `SECRET_KEY` | required | root key, 32 bytes base64 |
| `SECRET_KEY_PREVIOUS` | empty | previous root key during rotation |
| `BASE_URL` | `http://localhost:8080` | public origin |
| `OPERATOR_CONTACT` | required outside demo | email or URL in the User-Agent |
| `SITE_NAME` | `Journal` | header name |
| `MAX_USERS` | 30 | membership cap |
| `DB_PATH` | `journal.db` | SQLite file |
| `DATA_DIR` | `data` | caches and backups |
| `CACHE_MAX_MB` | 256 | combined image and userpic cache cap, least-recently-used eviction |
| `BACKUP_KEEP_DAILY` / `BACKUP_KEEP_WEEKLY` | 7 / 4 | backup retention |
| `RETENTION_LOCKED_DAYS` / `RETENTION_PUBLIC_DAYS` | 30 / 90 | remote content retention |
| `LJ_SOURCE` | `xmlrpc` | preferred LiveJournal read method |
| `CREDENTIALED_SERVICES` | `livejournal` | services allowed to link accounts and read friends pages |
| `LISTEN_ADDR` | `:8080` | HTTP listener |
| `METRICS_ADDR` | `127.0.0.1:9091` | Prometheus metrics; empty disables |
| `LOG_LEVEL` | `info` | log level |

### 13.2 Backups and restore

- A daily backup runs at a jittered time: `VACUUM INTO` a timestamped file under `DATA_DIR/backups`, keeping 7 daily and 4 weekly copies.
- `journal backup` runs one on demand.
- `journal restore <file>` runs with the server stopped. It verifies the backup (`integrity_check`, known schema version), keeps the current file as `*.pre-restore-<time>`, and swaps the backup in.
- Copying backups off the host is the operator's job. The README gives an example.

### 13.3 Migrations and upgrades

- Before applying pending migrations, the server writes a backup named for the target version.
- Each SQL migration file runs whole in one transaction, without statement splitting.
- A file may declare that it needs foreign keys off; the runner then disables them outside the transaction, runs `PRAGMA foreign_key_check` before commit, and re-enables them. Table rebuilds follow SQLite's documented rebuild procedure.
- Go data migrations are registered by version, are idempotent, and run after the SQL migration with the same version.
- After migrating, `PRAGMA integrity_check` must pass or the server refuses to start.
- Migrations are forward-only. Rollback means restoring the pre-migration backup.

### 13.4 Observability

- **Logs:** structured JSON (`log/slog`) to stdout, with a request ID per request (echoed as `X-Request-ID`), one access line per request (route pattern, status, duration), and one line per sync or feed job. Errors are never swallowed; template errors are logged and return 500.
- **Metrics:** Prometheus text on `METRICS_ADDR`:
  - HTTP requests and latency by route;
  - sync and feed runs by service and result;
  - freshness per account (age of the last good sync);
  - outbound requests by host class and status;
  - paused hosts;
  - cache bytes, database bytes, and the time of the last successful backup.
- **Admin page:** the same key figures in a readable form.

### 13.5 Container

The image runs as a non-root user. Compose sets a 512 MB memory limit, 1 CPU, a health check on `/healthz`, and keeps metrics on localhost only. Caddy terminates TLS.

## 14. Testing

Every check runs automatically, locally through one command and in CI on every push.

- **Unit tests,** table-driven, for every non-trivial function.
- **Store tests** on real temporary SQLite files.
- **Migration tests:**
  - apply every migration to a fixture database in the earliest supported shape and assert all rows survive;
  - apply them to an empty database;
  - `foreign_key_check` and `integrity_check` stay clean;
  - timestamps sort in time order.
- **Service tests** against recorded fixtures for XML-RPC, Atom, RSS, HTML, captcha, and block responses. No test touches the network.
- **Scheduler and outbound tests** with a fake clock:
  - jitter spread;
  - backoff never shortens;
  - per-host spacing;
  - `Retry-After`;
  - host pauses surviving restart;
  - the SSRF guard, including a DNS answer that changes between lookup and dial.
- **Visibility lifecycle tests:**
  - revocation after a complete walk;
  - credentialed copy wins over a feed copy;
  - public turning locked;
  - retention expiry;
  - cleanup.
- **Handler tests** per front-end, plus a shared contract suite run against both:
  - same URLs and effects;
  - CSRF rejection;
  - an authorization matrix of members × resources × actions;
  - `GET` never mutates;
  - security headers present.
- **Security tests:**
  - a full flow with sentinel secrets, then a scan of logs, responses, and the database file for the sentinels and their MD5;
  - vault wrong passphrase, AAD swap, passphrase change, and key rotation;
  - rate limits and dummy derivation.
- **Fuzz tests** for the sanitizer and rewriter (no script, event handler, or `javascript:` URL survives), the XML-RPC, RSS and Atom parsers, and handle and URL parsing.
- **Performance test** (build tag `perf`): seeded envelope-sized data, reading-page p95 under budget, and query plans without full scans of `entries`.
- **Browser tests** (build tag `e2e`, headless Chrome via chromedp) against the demo server:
  - sign-in;
  - expanding cuts;
  - list management with dialogs;
  - hide author;
  - Load more;
  - keyboard navigation;
  - the same flows with JavaScript disabled.
- **Container smoke test:** build the image, run it under the 512 MB limit, check `/healthz` and `/readyz`, and create an admin invite.
- **CI gates:** `go vet`, `staticcheck`, `govulncheck`, `go test -race`, short fuzz runs, perf, e2e, and container smoke.

## 15. Decisions

| Decision | Chosen | Alternatives and why not |
| --- | --- | --- |
| Build at all | a small private hub | Dreamwidth alone: imports your own journal, not friends' locked posts from other services. An RSS reader: cannot read friends-locked posts as an aggregate, and has no native posting or comments for the circle. |
| Storage | SQLite, one process | Postgres: an extra service to run, secure, and back up for 30 people, with no benefit inside the envelope. |
| Rendering | server-rendered HTML, progressive enhancement | SPA: more code, a build pipeline, a weaker CSP, and a no-JS fallback still needed. htmx: another dependency, when a small ES module covers the few enhancements. |
| Front-ends | two thin front-ends over one `app` layer, classic frozen at parity | CSS theming of one template set: cannot express different page composition, dialogs, and fragments without making the HTML-only UI carry JS-oriented markup. Modern only: loses the minimal HTML-only reference that doubles as the accessibility and no-JS baseline. The cost is a second template set and thin controllers; all logic and most tests live in `app` and the shared contract suite. |
| Credential protection | passphrase vault for passwords, server key for tokens | Server key only: a database plus key leak exposes every password. Passphrase only: sync stops whenever members are signed out. |
| Remember password | per account, on by default | Tokens only: members re-enter passwords whenever a token expires. Offered as the opt-out. |
| Service abstraction | `Service` interface with capabilities, plus a generic `feed` spoke | LiveJournal-only code: the second and third services would fork it; the feed spoke proves the boundary with a different protocol. |
| Timestamps | fixed-width UTC text | Integer epoch: needs every table rebuilt and makes the database harder to inspect by hand. |
| Client identity | honest User-Agent with operator contact | Browser impersonation: evades the service's own controls and contradicts its bot policies. |

## 16. Conventions

- Migrations are append-only; an applied migration is never edited.
- Timestamps are written only through the store's time formatter.
- No plaintext secret in logs, errors, URLs, or the database; errors are scrubbed before logging.
- `GET` is safe; every POST checks CSRF; every JavaScript action has a form equivalent.
- Service hosts and protocol details live only in spokes.
- Remote services are read-only.
- Original assets only; the site name comes from config.
