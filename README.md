# lessarr

Bad media encoding manager. Decides what in your library actually needs
re-encoding, and — eventually — does it.

Single process, single node, one config file. No server/node split, no plugin
graph, no opaque queue. Every decision comes with a reason you can read.

## Status

**M0.** Read-only analysis only. `probe` and `stats` will tell you exactly what
lessarr would do to every file in your library, and write nothing. The encode,
watch and serve paths arrive in M1–M3.

When they do, writing stays opt-in per library. There is no global switch:

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
# What would happen to one file, and the exact command that would run.
lessarr probe /media/movies/Example.mkv

# Measure the video bitrate from real packets instead of inferring it.
lessarr probe --accurate /media/movies/Example.mkv

# Walk a whole library and report the projected outcome. Read-only.
lessarr stats --config config.toml --top 20 --reasons

# Check this host: GPU, drivers, encoders, permissions, filesystem. Reads
# nothing and removes the one temporary file it creates.
lessarr selftest /media

# Check a config file before trusting it.
lessarr config-validate --config config.toml
```

`stats` prints, per library, how many files land in each action and what the
projected size change is. That is the number to argue with before letting
lessarr write anything. Start with `config.example.toml`.

`selftest` is the first thing to run on a new machine. It does not assume the
hardware works — it loads the driver, runs a short synthetic encode through each
backend, decode-verifies the result, and reports the throughput. It also flags
source codecs that have no hardware decoder, because those need a different
filter chain and fail at runtime otherwise.

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
- **Already HEVC/AV1** → skipped by default. Only reconsidered when bits per
  pixel per frame crosses a per-resolution threshold, and even then M1 will
  measure by sample-encoding rather than trusting a prediction.

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
