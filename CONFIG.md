# Configuration

lessarr reads one TOML file. Unknown keys are rejected, so a typo fails at
startup instead of silently falling back to a default. Start from
[`config.example.toml`](config.example.toml).

When running in Docker, paths in the config are **container paths**. The host
paths are set in `/opt/lessarr/.env` and mounted into those container paths.

Validate a file with:

```sh
lessarr config-validate --config /config/config.toml
```

Inside the container:

```sh
sudo docker exec lessarr /usr/local/bin/lessarr \
  config-validate --config /config/config.toml
```

## Durations and sizes

- Duration values are strings such as `"30m"`, `"48h"`, or `"720h"`.
- `*_mb` values are mebibytes (MiB).
- `*_gb` values are stored as gibibytes (GiB), despite the key name.
- Audio bitrates are strings such as `"640k"` or `"256k"`.

## `[server]`

| Key | Default | Meaning |
|---|---|---|
| `listen` | `0.0.0.0:8585` | Dashboard/API address. |
| `db` | `/config/lessarr.db` | SQLite database path. |
| `log_level` | `info` | Accepted for configuration compatibility; currently informational. |
| `username` | empty | Basic Auth username. Must be set with `password`. |
| `password` | empty | Basic Auth password. Must be set with `username`. |
| `webhook_url` | empty | Optional JSON webhook for failures and mount events. |

`/healthz` and `/readyz` are always unauthenticated. `/api/health`, the
dashboard, and other API routes use Basic Auth when credentials are configured.

Webhook events include `job_failed`, `mount_failed`, and `mount_recovered`.

## `[encoder]`

| Key | Default | Meaning |
|---|---|---|
| `backend` | `auto` | `auto`, `vaapi`, `qsv`, or `software`. |
| `device` | `/dev/dri/renderD128` | Render device for hardware backends. |
| `workers` | `1` | Number of concurrent encode workers. Keep at 1 on low-power hosts. |
| `nice` | `15` | Accepted for compatibility; process priority is currently managed by the host/container. |
| `decide_by` | `heuristic` | `heuristic` or `sample`; currently informational in this build. |

`auto` selects the best available backend. VAAPI is the normal production path
on Intel hardware; `software` uses `libx265` and is much slower.

## `[audio]`

| Key | Default | Meaning |
|---|---|---|
| `keep_languages` | `["eng"]` | Languages to retain. Use ISO 639-2/B codes such as `eng`, `fre`, `jpn`. |
| `keep_default_track` | `true` | Keep the track marked default. |
| `keep_original_language` | `true` | Keep the original language inferred from the filename. |
| `lossless_to_eac3` | `true` | Convert retained lossless tracks to E-AC-3 when no lossy track in that language already exists. |
| `eac3_bitrate_51` | `"640k"` | E-AC-3 bitrate for 6+ channel tracks. |
| `eac3_bitrate_20` | `"256k"` | E-AC-3 bitrate for stereo tracks. |

A lossless track is dropped instead of converted when a kept lossy track in the
same language already exists. Mono conversion uses 128k. At least one audio
track is always retained.

## `[subtitles]`

| Key | Default | Meaning |
|---|---|---|
| `keep_languages` | `["eng", "fre"]` | Subtitle languages to retain. |
| `keep_forced` | `true` | Always retain forced subtitle tracks. |

Subtitle tracks are copied, not OCR'd. Image subtitles are retained only when
their language or forced flag matches policy.

## `[trash]`

| Key | Default | Meaning |
|---|---|---|
| `retention` | `"48h"` | Maximum age of a retained original. `0` disables age expiry. |
| `max_size_gb` | `30` | Total trash cap in GiB. `0` disables the size cap. |

lessarr keeps the newest original per source path and deletes older or excess
originals according to this policy. Deleting a trash item from the UI is
explicit and permanent.

## `[comparisons]`

| Key | Default | Meaning |
|---|---|---|
| `retention` | `"720h"` | Maximum age of generated comparison galleries. `0` disables age expiry. |
| `max_size_gb` | `2` | Total comparison-image cap in GiB. `0` disables the cap. |

Comparison PNGs are generated for transcodes, not remuxes.

## `[[library]]`

At least one library is required. Library names must be unique. Nested
libraries are matched by the longest path prefix.

| Key | Default | Meaning |
|---|---|---|
| `name` | required | Display name and queue key. |
| `path` | required | Container path to scan. |
| `write` | `false` | Allow lessarr to modify files. False means observe only. |
| `mode` | `transcode` | `transcode`, `remux`, or `monitor`. |
| `max_long_edge` | `0` | Cap the longer image edge. `0` preserves source resolution. Never upscales. |
| `min_long_edge` | `480` | Skip files whose longer edge is below this value. |
| `icq` | `24` | Quality target, 1–51. Lower is better/larger. VAAPI uses CQP; QSV uses global quality; x265 uses CRF. |
| `preset` | `slow` | QSV/x265 preset. VAAPI has no preset setting. |
| `validation` | `sample` | `sample` decodes five short sections plus packet-scans remuxes; `full` decodes every frame. |
| `min_savings_percent` | `25` | Reject a transcode that saves less than this percentage. Range 1–95. |
| `min_remux_bytes` | `100` | Minimum audio savings, in MiB, for a remux without a container fix. |
| `min_duration` | `"3m"` | Skip media shorter than this. |
| `min_size_mb` | `50` | Skip files smaller than this. |
| `skip_if_newer_than` | `"10m"` | Wait for recent imports to settle before scanning. |
| `hdr` | `skip` | `skip` excludes HDR10/HLG/Dolby Vision; `passthrough` allows them through normal policy. |
| `allow_10bit` | `false` | False skips 10-bit HEVC entirely. True permits copy/remux; HEVC is still never re-encoded. |
| `free_space_factor` | `1.15` | Require this multiple of the source size free before encoding. Must be >= 1. |
| `ignore` | empty | Filename/path glob patterns to skip. |

### Modes

- `transcode`: full policy, including remux and video transcode.
- `remux`: audio/subtitle/container changes only. Video that needs re-encoding is skipped.
- `monitor`: scan and record only; no jobs act.

### Ignore patterns

Patterns are checked against both the file's base name and full path using Go
`filepath.Match`. `*` does not cross directory separators, so simple patterns
work best:

```toml
ignore = ["*sample*", "*.partial", "*.tmp"]
```

## Minimal example

```toml
[server]
listen = "0.0.0.0:8585"
db     = "/state/lessarr.db"

[encoder]
backend = "vaapi"
device  = "/dev/dri/renderD128"

[[library]]
name  = "Movies"
path  = "/media/Movies"
write = false
```

Everything omitted here uses the defaults above.
