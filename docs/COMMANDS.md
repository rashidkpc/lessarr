# Command reference

The production container is named `lessarr`. Run commands inside it with:

```sh
sudo docker exec lessarr /usr/local/bin/lessarr <command> ...
```

For one-off commands, set the host paths from your local `.env` first:

```sh
set -a
. /opt/lessarr/.env
set +a

sudo docker run --rm --network host \
  --device "$LESSARR_DEVICE" \
  -v "$LESSARR_CONFIG":/config/config.toml:ro \
  -v "$LESSARR_STATE":/state \
  -v "$LESSARR_MEDIA":/media \
  ghcr.io/rashidkpc/lessarr:edge-bookworm <command> ...
```

Paths after the colons are container paths; host paths come from `.env`.

## Daemon

```sh
lessarr run --config /config/config.toml
lessarr run --config /config/config.toml --once
```

`run` scans, queues, encodes, serves the dashboard, and runs maintenance loops.
`--once` scans and records decisions, then exits.

## Queue

```sh
lessarr status --config /config/config.toml
lessarr pause  --config /config/config.toml
lessarr resume --config /config/config.toml
```

The dashboard also supports pause/resume, queue prioritization, retry, restore,
skip, and unskip.

## Inspect media

```sh
lessarr probe /media/Movies/Example.mkv
lessarr probe --accurate /media/Movies/Example.mkv
lessarr stats --config /config/config.toml --top 20 --reasons
```

`probe` prints the decision and exact FFmpeg command for one file. `--accurate`
samples real packets for bitrate. `stats` walks a library read-only and reports
projected actions and savings.

## Validate and calibrate

```sh
lessarr selftest /media
lessarr config-validate --config /config/config.toml
lessarr calibrate --qps 18,22,26,30 --segments 3 --segment-seconds 10 \
  /media/Movies/Example.mkv
```

`selftest` checks GPU, drivers, encoders, permissions, and filesystem behavior.
`calibrate` compares VAAPI quality/size curves in local scratch space.

## Single-file work

```sh
lessarr once --config /config/config.toml /media/Movies/Example.mkv
lessarr once --config /config/config.toml --backend software --force \
  /media/Movies/Example.mkv
```

With `write = false` the command reports the decision without modifying the
file. `--force` is an explicit one-file override and still retains the original.

## Analysis

```sh
lessarr analyze --config /config/config.toml 42
```

Packet-scans the retained original and replacement, attributes size changes to
video/audio/subtitles/container, and records sampled SSIM.

## Version

```sh
lessarr version
```

Reports the embedded git revision.
