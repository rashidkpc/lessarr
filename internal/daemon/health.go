package daemon

import (
	"fmt"
	"os"
	"time"

	"github.com/rashidkpc/lessarr/internal/health"
)

const scanStaleAfter = 30 * time.Minute

// Health reports cached checks plus cheap live queue/database checks.
func (d *Daemon) Health() health.Snapshot {
	s := health.Snapshot{
		Backend: d.backend.Name,
		Device:  d.cfg.Encoder.Device,
	}

	counts, err := d.store.Counts()
	if err != nil {
		s.Database = health.Check{Error: err.Error()}
	} else {
		s.Database = health.Check{OK: true}
		s.Counts.Pending = counts.Pending
		s.Counts.Running = counts.Running
	}
	ready, err := d.store.CountReady(time.Now(), d.writable...)
	if err != nil && s.Database.OK {
		s.Database = health.Check{Error: err.Error()}
	} else {
		s.Counts.Ready = ready
	}
	if paused, err := d.store.Paused(); err == nil {
		s.Paused = paused
	}
	s.WritesEnabled = len(d.writable) > 0

	d.mountMu.Lock()
	mountChecked, mountDown, mountErr := d.mountChecked, d.mountDown, d.mountErr
	d.mountMu.Unlock()
	switch {
	case !mountChecked:
		s.Mount = health.Check{Error: "mount check has not run yet"}
	case mountDown:
		s.Mount = health.Check{Error: mountErr}
	default:
		s.Mount = health.Check{OK: true}
	}

	if d.backend.Name == "" {
		s.Encoder = health.Check{Error: "no encoder backend selected"}
	} else if d.backend.Name != "software" {
		if _, err := os.Stat(d.cfg.Encoder.Device); err != nil {
			s.Encoder = health.Check{Error: fmt.Sprintf("%s: %v", d.cfg.Encoder.Device, err)}
		} else {
			s.Encoder = health.Check{OK: true}
		}
	} else {
		s.Encoder = health.Check{OK: true}
	}

	d.scanMu.Lock()
	lastScan := d.lastScan
	d.scanMu.Unlock()
	s.LastScan = lastScan
	switch {
	case lastScan.IsZero():
		s.Scan = health.Check{Error: "no successful scan yet"}
	case time.Since(lastScan) > scanStaleAfter:
		s.Scan = health.Check{Error: fmt.Sprintf("last successful scan was %s ago", time.Since(lastScan).Round(time.Second))}
	default:
		s.Scan = health.Check{OK: true}
	}

	s.OK = s.Database.OK && s.Mount.OK && s.Encoder.OK && s.Scan.OK
	return s
}
