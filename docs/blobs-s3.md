# Files in an S3 bucket (Cloudflare R2)

By default the server keeps files in `GREENGRAM_DATA_DIR/blobs`. With
`GREENGRAM_BLOBS=s3` they live in an S3-compatible bucket instead, one
object per file named by its sha256, and a disk cache keeps the files
being read (stickers, emoji, avatars, gift art) close. The bucket is the
only copy that matters: the cache can be deleted at any time.

## Settings

| variable | |
|---|---|
| `GREENGRAM_BLOBS` | `disk` (default) or `s3` |
| `GREENGRAM_S3_ENDPOINT` | `https://<account id>.r2.cloudflarestorage.com` |
| `GREENGRAM_S3_BUCKET` | the bucket, e.g. `greengram-media` |
| `GREENGRAM_S3_REGION` | default `auto` (right for R2) |
| `GREENGRAM_S3_PREFIX` | optional, in front of every key, e.g. `blobs/` |
| `GREENGRAM_S3_ACCESS_KEY_ID` | the R2 token's access key id |
| `GREENGRAM_S3_SECRET_FILE` | a file holding the secret access key (or `GREENGRAM_S3_SECRET_ACCESS_KEY`) |
| `GREENGRAM_BLOB_CACHE_DIR` | default `GREENGRAM_DATA_DIR/blobcache` |
| `GREENGRAM_BLOB_CACHE_MB` | cache cap, default 2048; 0 turns the cache off |

`ggstickers`, `ggcollectibles` and `ggblobs` read the same variables.

On R2: one bucket, and an API token with **Object Read & Write** on that
bucket only. Nothing in the bucket is public; clients download through
the server.

With the wrong key or bucket the server refuses to start. If the bucket
doesn't answer at startup it starts anyway and serves what's cached. While
the bucket is down, downloads of anything not cached and all uploads fail
with `STORAGE_UNAVAILABLE` within seconds; after five failures in a row
the server stops asking for 10 s.

Uploads are signed with their sha256, which the bucket checks, so an
object either holds exactly the bytes its key names or doesn't exist.
Files over 64 MB go up in 16 MB parts. An upload that fails halfway is
aborted; R2 also drops unfinished ones after 7 days.

## Moving an instance from disk to the bucket

1. Set the `GREENGRAM_S3_*` variables (not `GREENGRAM_BLOBS` yet).
2. Copy: `ggblobs copy` reads `GREENGRAM_DATA_DIR/blobs`, uploads what's
   missing and downloads and hashes every object. Run it again until it
   reports `failed 0`; each run only uploads what's still missing.
3. Stop the server, then `ggblobs copy -quick` for what was uploaded
   meanwhile (seconds: everything else is there already).
4. Check: `ggblobs verify` (or `ggblobs verify -hash`) needs
   `GREENGRAM_DATABASE_URL` and says `verify passed` when every key the
   database references is in the bucket with its size.
5. Set `GREENGRAM_BLOBS=s3` and start the server.
6. Keep `data/blobs` until you're sure; delete it by hand later.

Going back: unset `GREENGRAM_BLOBS`. Files uploaded while on s3 are only in
the bucket then.
