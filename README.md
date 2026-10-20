# lessarr

Bad media encoding manager. Decides what in your library actually needs
re-encoding, and — eventually — does it.

Single process, single node, one config file. No server/node split, no plugin
graph, no opaque queue. Every decision comes with a reason you can read.

## Status

**M2.** `run` is the long-running daemon: it polls the libraries, records every
decision, persists a SQLite queue, processes one job at a time and serves a
status page. `probe` and `stats` remain read-only. `once` can encode/remux one
file after validation and retain the original in `.lessarr-trash`; it is
withheld by default because each library has `write = false`. `calibrate`
measures an offline VAAPI quality/size curve without modifying its source.

Writing stays opt-in per library:

```toml
[[library]]
name  = "Movies"
path  = "/media/movies"
# write = false is the default; set true to let lessarr modify this library
```

A library with `write = false` is not ignored — it is still probed, decided and
recorded, so the report shows exactly what would have happened. The switch is
per library because getting started is staged: enable one, watch it, then enable
the rest.

## What it does about

| | |
|---|---|
| Encoder | Intel VAAPI, preferring hardware; QSV fallback; software for tests |
| Target | HEVC 8-bit, MKV, direct-play friendly |
| HDR / Dolby Vision | never transcoded — skipped by policy |
| 10-bit HEVC | never re-encoded (hardware here outputs 8-bit only) |
| Prior Intel hardware HEVC | skipped when the stream tag identifies `hevc_vaapi` or `hevc_qsv` |
| Audio | keeps default/original-language + English; lossless → E-AC-3 unless a lossy track already covers it |
| Subtitles | keeps forced tracks + English + French; no OCR |
| Already-good files | skipped, with the passing checks named |

The point of the decision engine is that "needs re-encoding" is a checklist, not
a vibe. A file is skipped unless something *specific* is wrong with it, and the
reason string says which check failed.

## Build

```sh
go build ./cmd/lessarr
```

Requires Go 1.24. `ffprobe` must be on `PATH` at runtime.

## Use

```sh
# Long-running daemon: scan, queue, encode, and serve http://localhost:8585.
lessarr run --config config.toml

# Scan once, record decisions, and exit. Useful before turning writes on.
lessarr run --config config.toml --once

# Inspect or control the queue without the dashboard.
lessarr status --config config.toml
lessarr pause  --config config.toml
lessarr resume --config config.toml

# Attribute a completed job's savings and measure sampled SSIM.
lessarr analyze --config config.toml 42

# What would happen to one file, and the exact command that would run.
lessarr probe /media/movies/Example.mkv

# Measure the video bitrate from real packets instead of inferring it.
lessarr probe --accurate /media/movies/Example.mkv

# Walk a whole library and report the projected outcome. Read-only.
lessarr stats --config config.toml --top 20 --reasons

# Check this host: GPU, drivers, encoders, permissions, filesystem. Reads
# nothing and removes the one temporary file it creates.
lessarr selftest /media

# Compare VAAPI CQP settings on three 10-second real-media segments.
# Outputs go to local temporary scratch and are removed on completion.
lessarr calibrate --qps 18,22,26,30 --segments 3 --segment-seconds 10 \
  /media/Movies/Example.mkv

# Process one file. With write=false this reports the decision and does nothing.
lessarr once --config config.toml /media/movies/Example.mkv

# Explicit one-file test override; the original is retained in .lessarr-trash.
lessarr once --config config.toml --backend software --force /path/to/test-copy.mkv

# Check a config file before trusting it.
lessarr config-validate --config config.toml
```

`stats` prints, per library, how many files land in each action and what the
projected size change is. That is the number to argue with before letting
lessarr write anything. Start with `config.example.toml`.

`run` is the Tdarr replacement: a polling scan (because CIFS does not deliver
inotify events), a durable SQLite queue, one worker, retries, and a read-only
status page at `/` showing what is running, queued, completed, or failed.
Candidates are queued even when a library has
`write = false`; the worker simply refuses to claim jobs from non-writable
libraries, so the queue is fully inspectable before writes are enabled. The
`/files` page lists every decision with action, reason, size, and queue state,
with filters for library, action, queued/running/not-queued, and path. The queue
survives restarts; interrupted jobs return to `pending`, and withheld jobs are
re-queued rather than discarded.

Validation defaults to `sample`: five 20-second decodes across the output, plus
a full packet scan for remuxes. Set `validation = "full"` on a library to decode
every frame instead. The UI shows each validation step and full-decode progress
when that mode is used.

`analyze <job-id>` packet-scans the retained original and the replacement, then
attributes the size change to video, audio, subtitles, and container overhead.
It also samples SSIM against the original. Results are stored on the job and
shown on its detail page.

`selftest` is the first thing to run on a new machine. It does not assume the
hardware works — it loads the driver, runs a short synthetic encode through each
backend, decode-verifies the result, and reports the throughput. It also flags
source codecs that have no hardware decoder, because those need a different
filter chain and fail at runtime otherwise.

`once` supports in-place MKV and MP4/M4V/MOV replacements. AVI/TS and other
formats that would need an extension change are refused rather than left with a
misleading extension. Replacements are validated, preserve file metadata, and
retain the original in `.lessarr-trash`.

`calibrate` selects evenly spaced segments near 10%, 50%, and 90% of a
progressive SDR source, encodes each at the requested VAAPI QPs, then reports
size, SSIM, and XPSNR. It writes only local scratch files (FFV1 reference clips
and HEVC samples), removes them afterward, and never writes beside or replaces
the source. Defaults are 10 seconds per segment and QPs 18, 22, 26, and 30.

## Docker

```sh
docker build --platform linux/amd64 -t ghcr.io/rashidkpc/lessarr:edge .
docker run --rm -v /path/to/media:/media:ro ghcr.io/rashidkpc/lessarr:edge selftest /media
```

For a low-power host specifically — including the Docker client quirks, the
compose project setup and a config you can paste — see **[docs/remote share.md](docs/remote share.md)**.

## How the decision is made

Three actions:

- **Skip** — nothing worth doing, with the reason recorded.
- **Remux** — copy the video untouched, rewrite the audio/subtitle tracks.
  Seconds of work, often gigabytes saved. Bar: 100 MB, absolute.
- **Transcode** — re-encode the video with hardware, plus any track changes.

The engine is split into two branches, because guessing is only unsafe in one of
them:

- **Not HEVC/AV1** → almost always worth re-encoding. H.264→HEVC saves 40–60%,
  MPEG-2/VC-1/Xvid→HEVC saves 65–85%.
- **Already HEVC/AV1** → prior `hevc_vaapi`/`hevc_qsv` outputs are skipped. Other
  high-bitrate files are candidates, but their video savings are marked
  unmeasured and excluded from projections until sample encoding exists.

Efficiency is normalized as `bpp = video_bitrate / (width × height × fps)`,
with the video bitrate sampled from real packets when the container does not
record one — which MKV usually does not.

Re-encode loops are prevented by four independent guards: a provenance tag
written into every output, a database record, a higher savings bar for
already-efficient codecs, and a per-file encode budget.

The library already contains many prior `hevc_vaapi` encodes. Those are skipped
to avoid stacking another lossy hardware generation. For other already-modern
video that crosses the high-bitrate threshold, `stats` marks video savings as
unmeasured and excludes them from the projection; codec-generation projections
(for example H.264→HEVC) remain modeled, not measured.

Resolution is preserved by default. When a cap is configured it applies to the
**long edge**, never the height, so that a 2.40:1 film is not silently
upscaled into a 16:9 frame.

## Testing

```sh
make test               # unit tests; no ffmpeg, no GPU required
make test-integration   # adds the tests that shell out to ffmpeg
```

The decision table in `testdata/decisions.toml` covers 30+ synthetic files,
including codecs that cannot be synthesized (VC-1, DTS-HD MA, TrueHD, PGS) —
the engine only consumes probe facts, so hand-written cases cover them. The
hardware command lines are validated by golden argv tests, which is how a
machine with no GPU can still prove the VAAPI and QSV paths are correct.

## License

GPL-3.0. See `LICENSE` and `NOTICE` — the container image bundles GPL-licensed
FFmpeg, which is why that choice is the unambiguous one.
