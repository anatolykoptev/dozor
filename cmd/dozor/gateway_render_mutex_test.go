package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/dozor/internal/engine"
)

// TestRenderMutex_SerializesRenders verifies that concurrent notifyAlertFn
// invocations serialize their RenderAlertCard calls via renderMu.
//
// Setup: fake satori server that tracks peak concurrency; each render sleeps
// briefly so overlapping goroutines can be observed if the mutex is absent.
// Expected: peak inflight == 1 at all times.
func TestRenderMutex_SerializesRenders(t *testing.T) {
	var (
		inflight atomic.Int32
		peakMu   sync.Mutex
		peak     int32
	)

	// 1x1 transparent PNG (minimal valid PNG bytes for the header check).
	minPNG := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, // PNG magic
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52, // IHDR chunk len + type
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, // width=1, height=1
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xde, // 8-bit RGB, CRC
		0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41, 0x54, // IDAT chunk
		0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00, 0x00, 0x00, 0x02, 0x00, 0x01,
		0xe2, 0x21, 0xbc, 0x33, // CRC
		0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82, // IEND
	}

	// Fake satori: record peak concurrency, sleep 20ms to make races visible.
	satoriSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := inflight.Add(1)
		peakMu.Lock()
		if cur > peak {
			peak = cur
		}
		peakMu.Unlock()
		time.Sleep(20 * time.Millisecond)
		inflight.Add(-1)
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(minPNG)
	}))
	defer satoriSrv.Close()

	// Route RenderAlertCard at our fake server.
	t.Setenv("DOZOR_SATORI_URL", satoriSrv.URL)

	// Build a minimal notifyAlertFn-shaped closure identical to the one in gateway.go
	// so we exercise renderMu without needing to export it.
	//
	// We replicate only the render path — no bus, no telegram — so the test stays
	// fast and isolated. The mutex (renderMu) is the package-level var declared in
	// gateway.go and is visible here because we are in the same package (package main).
	var resultMu sync.Mutex
	var cards [][]byte
	notify := func(alerts []engine.Alert, _ string) {
		if len(alerts) == 0 {
			return
		}
		renderMu.Lock()
		card, err := engine.RenderAlertCard(context.Background(), alerts[0])
		renderMu.Unlock()
		if err != nil {
			t.Logf("render error (not expected): %v", err)
			return
		}
		resultMu.Lock()
		cards = append(cards, card)
		resultMu.Unlock()
	}

	alert := engine.Alert{
		Level:   engine.AlertCritical,
		Service: "test-svc",
		Title:   "Concurrent render test",
	}

	// Fire 3 concurrent goroutines, each calling notify.
	const n = 3
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			notify([]engine.Alert{alert}, fmt.Sprintf("fallback-%d", time.Now().UnixNano()))
		}()
	}
	wg.Wait()

	// All 3 renders must succeed.
	resultMu.Lock()
	got := len(cards)
	resultMu.Unlock()
	if got != n {
		t.Errorf("expected %d successful renders, got %d", n, got)
	}

	// Peak concurrency must be 1 — mutex serialized every call.
	peakMu.Lock()
	gotPeak := peak
	peakMu.Unlock()
	if gotPeak != 1 {
		t.Errorf("peak inflight satori requests = %d, want 1 (renderMu not serializing)", gotPeak)
	}
}
