# lessarr

Single-node media encoding manager. It scans libraries, explains what needs
re-encoding, queues the work, and replaces files only after validation and a
retained original.

Docker is the supported production deployment. The binary is for development and
diagnostics.

## Docker deployment

### Prerequisites

- Docker Engine and the Compose plugin.
- Intel render device at `/dev/dri/renderD128`.
- Media mounted at `/media`.
- A config at `/config/config.toml`.
- Persistent state at `/state`.

### Minimum install

```sh
git clone https://github.com/rashidkpc/lessarr.git
cd lessarr

sudo install -d -m 0755 /opt/lessarr
sudo install -m 0644 deploy/docker/compose.yaml /opt/lessarr/compose.yaml
sudo install -m 0644 deploy/docker/lessarr-compose.service /etc/systemd/system/lessarr-compose.service
sudo install -m 0644 deploy/docker/.env.example /opt/lessarr/.env

sudo systemctl daemon-reload
sudo systemctl enable --now lessarr-compose.service
```

The stack uses `ghcr.io/rashidkpc/lessarr:edge-bookworm` and host networking. The
systemd unit starts it after `/media` is mounted.

Check it once before enabling writes:

```sh
sudo docker run --rm --network host \
  --device /dev/dri/renderD128 \
  -v /media:/media \
  ghcr.io/rashidkpc/lessarr:edge-bookworm selftest /media
```

Then open `http://<host>:8585` and validate the config with:

```sh
sudo docker exec lessarr /usr/local/bin/lessarr \
  config-validate --config /config/config.toml
```

Updates:

```sh
cd /opt/lessarr
sudo docker compose pull
sudo systemctl restart lessarr-compose.service
```

### Configuration

Start from `config.example.toml`. The minimum shape is:

```toml
[server]
listen   = "0.0.0.0:8585"
db       = "/state/lessarr.db"
username = "change-me"
password = "change-me"

[encoder]
backend = "vaapi"
device  = "/dev/dri/renderD128"

[[library]]
name  = "Movies"
path  = "/media/Movies"
write = false
```

`write = false` still scans, probes, and records decisions. Only libraries with
`write = true` are modified.

## What it does

- **Skip**: nothing worth changing; records the passing checks.
- **Remux**: copies video untouched and rewrites audio/subtitles/container.
- **Transcode**: hardware H.264/MPEG-2/VC-1 → HEVC, with validation and a savings
  guard.
- HEVC and AV1 video are **copy-only**; they are never re-encoded.
- Replacements are atomic in the source directory, retain the original in
  `.lessarr-trash`, and are rejected if validation or savings checks fail.

## Operations

- Dashboard: `http://<host>:8585/` with `/files`, `/trash`, and `/metrics`.
- Basic Auth is optional; `/healthz` and `/readyz` are unauthenticated.
- `/api/health` exposes database, mount, encoder, scan, queue, and version
  state for monitoring.
- Trash: inspect tracked originals and orphans, delete individual items, or empty
  the trash. Restore, retry, skip, and unskip are available from the UI.
- A one-minute mount watchdog pauses new claims if a library becomes unreadable.
- SQLite state lives in `/state`; comparisons and backups are kept
  there too.

## Documentation

- **[docs/COMMANDS.md](docs/COMMANDS.md)** — CLI and container command reference.
- **[docs/HOME_ASSISTANT.md](docs/HOME_ASSISTANT.md)** — monitoring sensors.
- **[docs/remote share.md](docs/remote share.md)** — low-power host Docker client notes.
- **[config.example.toml](config.example.toml)** — all policy options.

## Development

```sh
make test       # unit tests; no GPU required
go build ./cmd/lessarr
```

The full command reference is in [docs/COMMANDS.md](docs/COMMANDS.md).

## License

GPL-3.0. See `LICENSE` and `NOTICE`. The image bundles GPL-licensed FFmpeg.
