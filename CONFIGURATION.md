# Configuration Guide

For advanced setups, environments where a Web UI is undesirable, or fully automated deployments, you can configure `gPhotos2Immich` strictly via a `config.json` file.

If you don't use the Web UI, you can disable it entirely by setting the `DISABLE_WEBUI=true` environment variable.

## Environment Variables

The `IMMICH_API_KEY` and `IMMICH_API_URL` environment variables override the corresponding `config.json` values at runtime. This is useful for key rotation: update the ENV var on your container and the next config read picks it up, even if a `config.json` with an older key exists. When neither ENV var is set, `config.json` is used unchanged. If neither the file nor the ENV vars provide credentials, the app waits for configuration via the Web UI (or exits if the Web UI is disabled).

## API Permissions

Your Immich API key needs these permissions (or use "All" for simplicity):

`asset.read` · `asset.upload` · `asset.view` · `album.create` · `album.read` · `album.update` · `albumAsset.create` · `user.read`

Add `asset.delete` **only** if you enable `reconcile.deleteMatchedDuplicates`.

`asset.view` is required to download thumbnails for perceptual comparison.

## Example `config.json`

```json
{
  "apiKey": "YOUR_IMMICH_API_KEY",
  "apiURL": "http://your-immich-ip:2283/api",
  "debug": false,
  "workers": 4,
  "albumWorkers": 3,
  "strictMetadata": false,
  "skipVideos": false,
  "dataDir": "./data",
  "dedup": {
    "mode": "smart",
    "dryRun": false,
    "similarityThreshold": 14,
    "ambiguityMargin": 4,
    "windowSeconds": 3,
    "wideWindowHours": 24,
    "thumbnailWidth": 300
  },
  "reconcile": {
    "enabled": true,
    "interval": "1h",
    "fullSweepInterval": "168h",
    "maxPerRun": 200,
    "maxDebtAge": "2160h",
    "deleteMatchedDuplicates": false,
    "retryBackoff": ["1h", "6h", "24h", "72h", "168h", "720h"],
    "reportPath": "./data/dedup-report.csv",
    "archivePath": "./data/dedup-ledger-archive.jsonl"
  },
  "googlePhotos": [
    {
      "url": "https://photos.app.goo.gl/YourAlbumLink1",
      "albumName": "Vacation 2023",
      "syncInterval": "12h",
      "enabled": true
    },
    {
      "url": "https://photos.app.goo.gl/ExistingAlbumLink",
      "immichAlbumId": "existing-album-uuid",
      "syncInterval": "1d",
      "enabled": false
    }
  ]
}
```

## Options

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `apiKey` | string | — | Immich API key (required). |
| `apiURL` | string | — | Immich API URL, e.g. `http://localhost:2283/api` (required). |
| `debug` | bool | `false` | Enable verbose debug logging. When disabled, displays clean progress bars with speed and ETA. |
| `workers` | int | `1` | Number of concurrent download/upload workers **per album**. |
| `albumWorkers` | int | `1` | Number of albums processed **concurrently**. |
| `strictMetadata` | bool | `false` | Skip items with missing/invalid dates instead of uploading with current date. |
| `skipVideos` | bool | `false` | Skip all video items entirely. |
| `dataDir` | string | `./data` | Directory for the state cache, ledger, status and reports. |

## Dedup Options (`dedup`)

Controls how already-present Immich assets are recognised.

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `mode` | string | `smart` | `smart` enables perceptual matching. `off` restores the original behaviour (always upload). |
| `dryRun` | bool | `false` | Perform matching and logging but write nothing to Immich: no upload, no album change, no deletion. |
| `similarityThreshold` | int | `14` | Maximum Hamming distance (out of 64) for two perceptual hashes to be considered the same photo. Raise if re-encoded photos are missed, lower if unrelated photos match. |
| `ambiguityMargin` | int | `4` | Minimum gap between the best and second best candidate. Below it, the item is treated as ambiguous and uploaded as a duplicate on purpose. |
| `windowSeconds` | int | `3` | Tight timestamp window used to find candidates. |
| `wideWindowHours` | int | `24` | Fallback timestamp window. `0` disables it. |
| `thumbnailWidth` | int | `300` | Width of the Google rendition used for hashing. Larger = more accurate, more bandwidth. |

## Reconciliation Options (`reconcile`)

Controls the background pass that fixes duplicates uploaded before the original was in Immich.

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `enabled` | bool | `true` | Enable the reconciliation loop. |
| `interval` | duration | `1h` | How often to check pending debts. |
| `fullSweepInterval` | duration | `168h` | How often to import all of the tool's existing uploads into the ledger. |
| `maxPerRun` | int | `200` | Maximum number of debts checked per pass. |
| `maxDebtAge` | duration | `2160h` | Maximum lifetime of a debt (90 days). `0` means infinite. Expired duplicates are kept and reported. |
| `deleteMatchedDuplicates` | bool | `false` | Delete the `gp_*` duplicate once a confident match is found. Requires `asset.delete`. |
| `retryBackoff` | list | `["1h","6h","24h","72h","168h","720h"]` | Delay between retries. The last value is reused once exhausted. |
| `reportPath` | string | `./data/dedup-report.csv` | CSV report location. |
| `archivePath` | string | `./data/dedup-ledger-archive.jsonl` | Where old resolved/expired ledger entries are moved. |

Durations accept `s`, `m`, `h`, `d` and `w`, so `7d` and `168h` are equivalent.

## Album Options

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `googlePhotos[].url` | string | — | Google Photos shared album link (required). |
| `googlePhotos[].albumName` | string | auto-detected | Override the album name in Immich. |
| `googlePhotos[].syncInterval` | string | `24h` | How often to re-check this album (e.g. `12h`, `2d`, `1w`). |
| `googlePhotos[].immichAlbumId` | string | — | Link to an existing Immich album by UUID instead of creating a new one. |
| `googlePhotos[].enabled` | bool | `true` | Set to `false` to keep the album configured without syncing it. |

## Web API Endpoints

When the Web UI is enabled, the embedded server provides the following endpoints:

| Endpoint | Method | Description |
| --- | --- | --- |
| `/api/sync` | `POST` / `GET` | Trigger an immediate sync of all configured albums. |
| `/api/sync?album=<url>` | `POST` / `GET` | Trigger an immediate sync of a single album. |
| `/sync_now` | `POST` / `GET` | Alias for `/api/sync`, convenient for Home Assistant buttons or curl commands. |
| `/api/reconcile` | `POST` / `GET` | Force a full reconciliation pass (includes expired debts). |
| `/api/status` | `GET` | Current progress, Immich connection, per-album status (`albums`), ledger counters (`debts`) and reconciliation settings (`reconcile`). |
| `/sync/status` | `GET` | Alias for `/api/status`. |
| `/api/logs` | `GET` | Return the latest application logs. |
| `/api/config` | `GET` / `POST` | Retrieve or update the application configuration. |

## Data Files

| File | Purpose |
| --- | --- |
| `sync-state.json` | Google item ID → Immich asset ID mapping. |
| `dedup-ledger.jsonl` | Append-only journal of duplicate debts. |
| `dedup-ledger-archive.jsonl` | Ledger entries archived after 30 days. |
| `dedup-report.csv` | History of created, resolved, expired and discovered duplicates. |
| `sync-status.json` | Per-album status used by the UI. |
