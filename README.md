# lessarr

Single-node media encoding manager. Docker is the supported deployment method.

## Prerequisites

- Docker Engine and the Compose plugin.
- A compatible render device.
- A media directory.
- A config file.
- A persistent state directory.

## Install

```sh
git clone https://github.com/rashidkpc/lessarr.git
cd lessarr

sudo install -d -m 0755 /opt/lessarr
sudo install -m 0644 deploy/docker/compose.yaml /opt/lessarr/compose.yaml
sudo install -m 0644 deploy/docker/lessarr-compose.service /etc/systemd/system/lessarr-compose.service
sudo install -m 0644 deploy/docker/.env.example /opt/lessarr/.env
# Edit /opt/lessarr/.env with your config, state, media, and device paths.

sudo systemctl daemon-reload
sudo systemctl enable --now lessarr-compose.service
```

The Compose file reads local paths from `/opt/lessarr/.env`. The container uses
`ghcr.io/rashidkpc/lessarr:edge-bookworm` and host networking. If your media
mount must be present before startup, add a local systemd drop-in with
`RequiresMountsFor=<media mount>`.

## Configure

Create the config file referenced by `LESSARR_CONFIG`. Use container paths for
the database and libraries:

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

Set `write = true` only for libraries lessarr is allowed to modify.

## Verify

```sh
set -a
. /opt/lessarr/.env
set +a

sudo docker run --rm --network host \
  --device "$LESSARR_DEVICE" \
  -v "$LESSARR_MEDIA":/media \
  ghcr.io/rashidkpc/lessarr:edge-bookworm selftest /media

curl -fsS http://127.0.0.1:8585/readyz
```

The first scan can take a couple of minutes; `/readyz` returns `503` until it
completes. Open `http://<host>:8585/` when it reports `ok`.

Check the mounted configuration:

```sh
sudo docker exec lessarr /usr/local/bin/lessarr \
  config-validate --config /config/config.toml
```

## Update

```sh
cd /opt/lessarr
sudo docker compose pull
sudo systemctl restart lessarr-compose.service
```

## Logs and status

```sh
sudo systemctl status lessarr-compose.service
sudo docker logs --tail 100 lessarr
sudo journalctl -u lessarr-compose.service -n 100
```
