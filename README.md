# lessarr

Single-node media encoding manager. Docker is the supported deployment method.

## Prerequisites

- Docker Engine and the Compose plugin.
- Intel render device at `/dev/dri/renderD128`.
- Media mounted at `/media`.
- A config at `/config/config.toml`.
- Persistent state at `/state`.

## Install

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

The service starts the Compose stack after `/media` is mounted. The
container uses `ghcr.io/rashidkpc/lessarr:edge-bookworm` and host networking.

## Configure

Create `/config/config.toml` before starting the service:

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

Set `write = true` only for libraries lessarr is allowed to modify. With
`write = false`, files are inspected but never changed.

## Verify

```sh
sudo docker run --rm --network host \
  --device /dev/dri/renderD128 \
  -v /media:/media \
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
