# S3 storage

`STORAGE_BACKEND=s3` stores TJA, original audio, WebP covers, and generated download ZIPs in a private S3 bucket. Configure `S3_BUCKET`, `AWS_REGION`, and a nonempty `S3_PREFIX`. Credentials follow the AWS SDK chain; use a role where available, otherwise keep the server env root-only. `STORAGE_DIR` is temporary staging only; the production container uses tmpfs and mounts no permanent local resource volume. The default `local` mode remains supported.

Schema 023 adds cover object metadata, archive metadata and pending-write recovery. Upload objects use unique keys and conditional PUT with SHA-256 verification. Failed/uncertain writes are reconciled after one day against all live references; committed resource deletions use the retryable `retired_files` queue. Replacing a chart keeps the existing reset semantics. Local mode still stores cover bytes in PostgreSQL.

## Download compatibility and direct URLs

Current TJA/audio/ZIP routes are `/api/v1/charts/{id}/{tja|audio|download}`. They stream from S3 through the API and preserve audio HEAD, Range, If-Range, ETag and conditional requests. Direct resource links bypass API bandwidth. Songs, downloads and score submission are identified by song ID only; bootstrap no longer advertises this with a `songIdOnly` field (removed 2026-10-07).

`game/bootstrap` advertises `resourceDownloadVersion=1` for S3. New clients can resolve:

```
GET /api/v1/charts/{chartId}/resources
```

The response has `chartId`, `expiresAt` and `resources` keyed by `tja`, `audio`, `download`, and optional `cover`. Each resource has `url`, `headUrl`, `sha256`, `size`, `contentType`. GET and HEAD have separate signatures, valid for 15 minutes; Range works on the GET URL. Resolve at download time and re-resolve on expiry. Verify size and SHA-256 before caching. Never attach the game Bearer token to an external resource request and never persist signed URLs in catalogs. Native clients need no browser CORS; web clients require a separately reviewed bucket CORS configuration.

The endpoint resolves the current resources of a published/playable chart using only its chart ID. Clients neither submit nor receive a version ID in this resource contract. Use the returned hashes to identify cached file content; resolve again when the chart changes. Schema 024 removes all song revision rows and IDs, including score relationships. Version-addressed download routes are removed. An already issued URL is a bearer capability and may remain valid for up to 15 minutes after unpublishing; replacement deletes retired objects. The bucket remains private. CloudFront is not required for signed S3 downloads.

## Migrating local resources to S3

1. Back up PostgreSQL, file volume, compose/env and proxy configuration. Clone the database into an isolated name ending `_s3_test` and restore ownership to its own login role. Keep the source snapshot unchanged.
2. Build `./cmd/migrate-storage` for the server platform. Run with the clone's `DATABASE_URL`, destination AWS credentials and `-source`, `-bucket`, `-region`, `-prefix`. The default only validates source hashes and inventories resources; for older snapshots the migration tool reads legacy resource metadata solely for migration.
3. Add `-apply -activate` to copy every referenced file and cover and generate ZIPs for every current song. Every object is downloaded and checked against SHA-256 and size. Conditional PUT makes retries safe: conflicting existing bytes abort instead of overwriting. Activation is restricted to `_s3_test` databases. It first upgrades the clone to schema 024, then copies/verifies objects, and commits S3 metadata after verification. Never run activation on the only copy of a database. Cover bytes remain in the clone for rollback; new uploads store only S3 cover metadata.

## Song-only schema 024

The current code uses one `chart_data` row per song and no song revision IDs in charts, scores, difficulties, upload receipts, ZIPs or routes. Resource links and SHA-256 fields remain unchanged. Client handoff: [GAME_CLIENT_RESOURCE_DOWNLOAD.md](GAME_CLIENT_RESOURCE_DOWNLOAD.md).

Replacement validates and writes new objects, atomically updates current metadata and deletes the song's previous scores/difficulties/archive, then deletes unreferenced old TJA/audio/ZIP objects. Covers are independent. Deletion failures are persisted and retried. Random object IDs provide safe write isolation, not retained song revisions. New requests serialize on the song; retries reuse their original idempotency key. Score tombstones reject previously accepted, removed score retries. Newly submitted offline old plays cannot be distinguished server-side without a play-content identity; the client prechecks captured file hashes with the documented race limitation.
