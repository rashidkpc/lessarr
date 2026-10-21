# lessarr

Media transcoding manager. Probably not better than your shell script. You probably shouldn't use this. I probably shouldn't use this. Whatever. Vibe up your own nonsense. 

## Install

```sh
git clone https://github.com/rashidkpc/lessarr.git
cd lessarr

sudo install -d -m 0755 /opt/lessarr
sudo install -m 0644 deploy/docker/compose.yaml /opt/lessarr/compose.yaml
sudo install -m 0644 deploy/docker/.env.example /opt/lessarr/.env

# Edit /opt/lessarr/.env with your config, state, media, and device paths.
cd /opt/lessarr
sudo docker compose up -d
```

The Compose file reads local paths from `/opt/lessarr/.env`. The container uses
`ghcr.io/rashidkpc/lessarr:edge-bookworm` and host networking.

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

Set `write = true` only for libraries lessarr is allowed to modify. Full option
reference: [CONFIG.md](CONFIG.md).

## Update

```sh
cd /opt/lessarr
sudo docker compose pull
sudo docker compose up -d
```

## Logs and status

```sh
cd /opt/lessarr
sudo docker compose ps
sudo docker logs --tail 100 lessarr
```
