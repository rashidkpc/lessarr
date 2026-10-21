# Home Assistant monitoring

lessarr exposes a small JSON health endpoint intended for REST sensors. It is
monitoring-only; this configuration does not pause or resume the daemon.

## Health endpoint

```text
GET http://lessarr.example:8585/api/health
Authorization: Basic ...
```

`/api/health` is protected by the same Basic Auth as the dashboard. `/readyz`
is intentionally unauthenticated and returns `200` only when the database,
mount, encoder, and last scan are healthy.

Example response:

```json
{
  "version": "a6f5f3c",
  "time": "2026-10-21T15:00:00Z",
  "ok": true,
  "paused": false,
  "writes_enabled": true,
  "database": { "ok": true },
  "mount": { "ok": true },
  "encoder": { "ok": true },
  "scan": { "ok": true },
  "backend": "vaapi",
  "device": "/dev/dri/renderD128",
  "counts": { "pending": 55, "ready": 0, "running": 0 },
  "last_scan": "2026-10-21T14:58:00Z"
}
```

## configuration.yaml

Use your actual lessarr username and password.

```yaml
rest:
  - resource: http://lessarr.example:8585/api/health
    authentication: basic
    username: lessarr
    password: change-me
    scan_interval: 60
    sensor:
      - name: "lessarr health"
        value_template: >-
          {{ 'ok' if value_json.ok else 'failed' }}
        json_attributes:
          - paused
          - writes_enabled
          - counts
          - last_scan

      - name: "lessarr pending"
        value_template: "{{ value_json.counts.pending }}"
        state_class: measurement

      - name: "lessarr ready"
        value_template: "{{ value_json.counts.ready }}"
        state_class: measurement

      - name: "lessarr running"
        value_template: "{{ value_json.counts.running }}"
        state_class: measurement

  - resource: http://lessarr.example:8585/api/status
    authentication: basic
    username: lessarr
    password: change-me
    scan_interval: 60
    sensor:
      - name: "lessarr reclaimed 30d"
        value_template: "{{ (value_json.Saved30d / 1073741824) | round(1) }}"
        unit_of_measurement: GiB
        state_class: total_increasing

      - name: "lessarr library"
        value_template: "{{ (value_json.LibraryBytes / 1073741824) | round(1) }}"
        unit_of_measurement: GiB
        state_class: measurement

      - name: "lessarr trash"
        value_template: "{{ (value_json.TrashBytes / 1073741824) | round(1) }}"
        unit_of_measurement: GiB
        state_class: measurement

template:
  - binary_sensor:
      - name: "lessarr healthy"
        state: "{{ is_state('sensor.lessarr_health', 'ok') }}"
        device_class: connectivity
```

## Failure notifications

lessarr can POST JSON events to a Home Assistant webhook. Set this in
`/config/config.toml`:

```toml
[server]
webhook_url = "http://homeassistant.example:8123/api/webhook/lessarr"
```

Then create an automation with a webhook trigger and inspect
`trigger.json.event` (`job_failed`, `mount_failed`, or `mount_recovered`).
