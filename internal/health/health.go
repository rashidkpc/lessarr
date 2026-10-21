// Package health describes the daemon's readiness for monitoring.
package health

import "time"

type Check struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type Counts struct {
	Pending int `json:"pending"`
	Ready   int `json:"ready"`
	Running int `json:"running"`
}

type Snapshot struct {
	OK            bool      `json:"ok"`
	Paused        bool      `json:"paused"`
	WritesEnabled bool      `json:"writes_enabled"`
	Database      Check     `json:"database"`
	Mount         Check     `json:"mount"`
	Encoder       Check     `json:"encoder"`
	Scan          Check     `json:"scan"`
	Backend       string    `json:"backend,omitempty"`
	Device        string    `json:"device,omitempty"`
	Counts        Counts    `json:"counts"`
	LastScan      time.Time `json:"last_scan,omitempty"`
}
