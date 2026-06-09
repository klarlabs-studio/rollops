//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/rolloffs/internal/analysis"
)

// TestPrometheusAnalysis_Live drives the metric-analysis seam against a real
// Prometheus (docker-compose) scraping itself, proving the provider + the
// CEL-driven analyzer end to end.
func TestPrometheusAnalysis_Live(t *testing.T) {
	addr := getenv("PROM_ADDR", "")
	if addr == "" {
		t.Skip("PROM_ADDR not set; run via test/integration/run.sh")
	}
	prov := analysis.Prometheus{Addr: addr}
	ctx := context.Background()

	// Wait for the first self-scrape so `up` exists and equals 1.
	deadline := time.Now().Add(30 * time.Second)
	var up float64
	for time.Now().Before(deadline) {
		if v, err := prov.Query(ctx, "sum(up)"); err == nil && v >= 1 {
			up = v
			break
		}
		time.Sleep(time.Second)
	}
	if up < 1 {
		t.Fatalf("prometheus self-target never came up (sum(up)=%v)", up)
	}

	// Analyzer over the live metric: healthy condition passes.
	pass, err := analysis.New(prov, analysis.Template{
		Metrics:   []analysis.Metric{{Name: "up", Query: "sum(up)"}},
		Condition: "up >= 1",
		Count:     3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r := pass.Run(ctx); !r.Passed {
		t.Fatalf("healthy condition should pass against live prometheus; reason=%q", r.Reason)
	}

	// A breaching condition fails (the analyzer would trigger a rollback).
	fail, _ := analysis.New(prov, analysis.Template{
		Metrics:      []analysis.Metric{{Name: "up", Query: "sum(up)"}},
		Condition:    "up < 0.5",
		Count:        2,
		FailureLimit: 0,
	})
	if r := fail.Run(ctx); r.Passed {
		t.Fatal("breaching condition should fail against live prometheus")
	}
}
