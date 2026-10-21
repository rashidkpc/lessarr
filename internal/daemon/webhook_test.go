package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rashidkpc/lessarr/internal/config"
	"github.com/rashidkpc/lessarr/internal/queue"
)

func TestNotifyFailureWebhook(t *testing.T) {
	payloads := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		payloads <- payload
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Server.WebhookURL = server.URL
	d := &Daemon{cfg: &cfg, log: io.Discard}
	d.notifyFailure(&queue.Job{ID: 12, Path: "/media/a.mkv", Library: "Movies", Action: "transcode", Attempts: 3}, "boom", true)

	select {
	case payload := <-payloads:
		if payload["event"] != "job_failed" || payload["id"] != float64(12) || payload["error"] != "boom" {
			t.Fatalf("payload = %#v", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("webhook was not delivered")
	}
}
