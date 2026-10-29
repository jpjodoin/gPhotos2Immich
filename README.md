# gPhotos2Immich (dedup fork)

Sync photos from Google Photos shared albums to your [Immich](https://immich.app) instance — automatically, on a schedule.

This is a fork of [warreth/gPhotos2Immich](https://github.com/warreth/gPhotos2Immich) that **stops creating duplicates**. Google Photos serves re-encoded copies for shared albums, so their checksums never match your originals and Immich's hash-based dedup cannot help. This fork recognises the same photo perceptually and simply adds your existing asset to the album instead of uploading a copy.

---

## What this fork adds

- **Smart dedup** — before downloading an item, a small rendition of it is compared with likely candidates already in Immich (same timestamp window, dimensions, perceptual hash). When a single confident match is found, the existing asset is added to the album and **nothing is uploaded**.
- **Conservative by design** — if several candidates look alike, the tool uploads a duplicate on purpose and logs it. It never guesses an album assignment.
- **Duplicate reconciliation** — every upload is recorded as a "debt" in a persistent ledger. A background pass retries it on a backoff schedule; when the original finally appears in Immich (for example because you uploaded it there later), the original is added to the album and the duplicate can be deleted.
- **Full sweep** — on startup and every week, the tool imports every asset it has ever uploaded into the ledger, so duplicates created before this feature existed are caught too.
- **Debt expiry** — a duplicate is only tracked for `maxDebtAge` (default 90 days). Expired duplicates are kept (they may be the only copy) and reported.
- **Per-album status** — last run result, matched/uploaded/skipped/failed/ambiguous counts, next run, and errors, right in the UI.
- **Per-album enable toggle and "Sync" button** — keep an album configured without syncing it.
- **Dry-run mode** — perform the matching and see the report without writing anything to Immich.

---

## Quick Start

1. Build and run the fork with Docker Compose:

   ```yaml
   services:
     gphotos2immich:
       build: .
       container_name: gphotos2immich
       network_mode: "host" # Fixes DNS resolution and natively exposes port 8080 for the Web UI
       restart: unless-stopped
       environment:
         - PORT=8080 # Port for the Web UI
         - DISABLE_WEBUI=false # Set to true to fully disable the Web UI
         # - IMMICH_API_KEY=your-key # Overrides config.json when set (e.g. for key rotation)
         # - IMMICH_API_URL=http://immich-server:2283/api # Overrides config.json when set
       volumes:
         - ./config.json:/app/config.json # Optional, will be created by Web UI if omitted
         - ./data:/app/data # Persistent dedup cache, ledger and status
   ```

   ```bash
   docker compose up -d --build
   ```

2. Open **[http://localhost:8080](http://localhost:8080)** and configure your Immich API details and Google Photos shared album links. The app hot-reloads when you save changes.

> **Permissions Note:** The container runs as an unprivileged user (`app`, UID 1000).
> - **Default behavior:** If you run the container without specifying a `user:`, it will automatically fix permissions on mounted files at startup and drop to the `app` user.
> - **Explicit user:** If you specify `user: "1000:1000"` in your compose file, ensure the mounted `config.json` and `data/` on the host are writable by UID 1000 (`chown 1000:1000 config.json data`).

> [!IMPORTANT]
> The built-in Web configuration UI is not password-protected. Do **NOT** expose the port to the public internet or untrusted networks!

> **Heads Up!** If you prefer not to use a Web UI, you can fully disable it by setting `DISABLE_WEBUI=true` and configuring via `config.json`. See the [Configuration Document](CONFIGURATION.md).

---

## API Key Permissions

Generate an Immich API key with the following permissions (or just select "All"):

`asset.read` · `asset.upload` · `asset.view` · `album.create` · `album.read` · `album.update` · `albumAsset.create` · `user.read`

`asset.view` is used to download thumbnails for perceptual comparison. Only add `asset.delete` if you enable `reconcile.deleteMatchedDuplicates`.

---

## How duplicate handling works

For every Google Photos item, in order:

1. **Known item?** The album contents, the tool's own `gp_*` assets and the local state cache are checked first. Hit = skip.
2. **Fetch a small rendition** (`=w300`, a few KB instead of the full file) and compute a 64-bit perceptual hash.
3. **Find candidates in Immich** around the item's taken date: ±3 s, then ±2 min, then ±24 h (only if that window stays under 25 candidates). Trashed assets, hidden assets and the tool's own `gp_*` uploads are ignored.
4. **Compare**: dimensions/aspect ratio must be compatible, then the perceptual hashes are compared (Hamming distance).
   - One clear winner → the existing asset is added to the album. **No upload.**
   - Several close candidates → upload a duplicate and log it for reconciliation. Never guess.
   - No candidate → upload.
5. **Record a debt** for every upload. A background pass retries it later; once the original shows up, it is added to every album that contained the duplicate.

Decisions and outcomes are written to `data/dedup-report.csv`.

### Recommended rollout

1. Start with `"dedup": { "dryRun": true }` on one album, run a sync, and check the log/status counts.
2. If the match rate looks right, set `dryRun: false`.
3. Let the reconciliation pass run for a while and inspect `data/dedup-report.csv`.
4. Only then consider `"reconcile": { "deleteMatchedDuplicates": true }`.

### Tuning

`dedup.similarityThreshold` (default 14/64) is the maximum Hamming distance for a match. Raise it if re-encoded photos are not recognised; lower it if unrelated photos get matched. `dedup.ambiguityMargin` (default 4) is the minimum gap between the best and second best candidate.

---

## Frequently Asked Questions

**Can this sync albums I don't own, or private albums?**
Only public shared album links work. The tool scrapes shared-album URLs without any Google authentication by design, so an album must be viewable by anyone with the link (test it in an incognito window). Private albums and links that require signing in return 404 and cannot be supported without full Google account authentication, which is out of scope.

**Will it delete my photos?**
Only if you explicitly set `reconcile.deleteMatchedDuplicates: true`, and only the `gp_*` copies it uploaded itself, after a confident match with one of your real assets. Original assets are never touched.

**What if the original arrives in Immich after the sync?**
That is exactly the case the reconciliation pass handles. The duplicate is kept for now, tracked in the ledger, and resolved when the original appears (within `maxDebtAge`).

**Why not use Immich's built-in duplicate detection?**
It compares SHA1 checksums. Google's shared-album renditions are re-encoded, so the checksums differ even for the same photo.

---

## Features

- **No Google API Keys:** scrapes directly from shared album URLs, so there's no Google Cloud setup.
- **Smart dedup:** recognises re-encoded copies and reuses your existing assets.
- **Duplicate reconciliation:** retroactively fixes duplicates created earlier or uploaded out of order.
- **Web UI:** per-album status, enable toggle, per-album "Sync", live logs, dry-run banner and a duplicate tracker.
- **Trigger APIs:** `POST /api/sync` (all albums), `POST /api/sync?album=<url>` (one album), `POST /api/reconcile` (full dedup pass). See [home-assistant.yaml](home-assistant.yaml).
- **Smart syncing:** full images and videos, correct "taken" dates, Google rate-limit handling.
- **Speed:** concurrent workers and a persistent cache.

---

## Data files

Everything lives in `dataDir` (default `./data`):

| File | Purpose |
| --- | --- |
| `sync-state.json` | Google item ID → Immich asset ID mapping, so nothing is re-uploaded. |
| `dedup-ledger.jsonl` | Append-only journal of duplicate debts. |
| `dedup-ledger-archive.jsonl` | Resolved/expired debts older than 30 days. |
| `dedup-report.csv` | Human-readable history: created, resolved, expired, discovered. |
| `sync-status.json` | Per-album status shown in the UI. |

---

## Development

```bash
# Run directly
go run main.go

# Run with Docker (build from source)
docker compose up --build --remove-orphans

# Run tests (offline; integration tests are skipped)
go test ./...

# Run tests including live integration tests against the real test album
GP2IMMICH_INTEGRATION=1 go test ./... -timeout 15m
```

## Licence

AGPL-3.0, inherited from the upstream project.
