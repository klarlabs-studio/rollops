package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// httpDoer is the slice of http.Client the provider needs (injectable in tests).
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Prometheus is a MetricsProvider backed by the Prometheus HTTP query API.
// Query returns a single scalar; a multi-series vector is rejected (#175) so a
// gate cannot silently pass on one arbitrary series. Use QuerySeries plus an
// explicit Metric.Aggregation when a multi-series result is intentional.
// Other backends (Datadog, CloudWatch, a custom metrics service) implement
// MetricsProvider the same way — Prometheus is just the first concrete one.
type Prometheus struct {
	Addr   string // e.g. http://prometheus:9090
	Client httpDoer
}

func (p Prometheus) client() httpDoer {
	if p.Client != nil {
		return p.Client
	}
	return http.DefaultClient
}

// promResponse is the subset of the Prometheus query response we consume.
type promResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string            `json:"resultType"`
		Result     []json.RawMessage `json:"result"`
	} `json:"data"`
	Error string `json:"error"`
}

// Query runs an instant query and returns its single sample's value.
// A vector with more than one series is an error: Prometheus does not guarantee
// ordering, and taking Result[0] would gate a rollout on an arbitrary series
// while a broken label value stays invisible (#175).
func (p Prometheus) Query(ctx context.Context, query string) (float64, error) {
	samples, err := p.QuerySeries(ctx, query)
	if err != nil {
		return 0, err
	}
	if len(samples) > 1 {
		return 0, multiSeriesError(query, samples)
	}
	return samples[0].Value, nil
}

// QuerySeries runs an instant query and returns every sample. Implements
// SeriesProvider so Metric.Aggregation can reduce a multi-series vector
// explicitly rather than by accident.
func (p Prometheus) QuerySeries(ctx context.Context, query string) ([]SeriesSample, error) {
	u := fmt.Sprintf("%s/api/v1/query?query=%s", p.Addr, url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("analysis: prometheus: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("analysis: prometheus status %d", resp.StatusCode)
	}

	var pr promResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return nil, fmt.Errorf("analysis: prometheus decode: %w", err)
	}
	if pr.Status != "success" {
		return nil, fmt.Errorf("analysis: prometheus error: %s", pr.Error)
	}
	if len(pr.Data.Result) == 0 {
		return nil, fmt.Errorf("analysis: prometheus query %q returned no data", query)
	}
	out := make([]SeriesSample, 0, len(pr.Data.Result))
	for _, raw := range pr.Data.Result {
		s, err := parseSample(pr.Data.ResultType, raw)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func multiSeriesError(query string, samples []SeriesSample) error {
	labels := formatLabels(samples[0].Labels)
	return fmt.Errorf("analysis: prometheus query %q returned %d series (e.g. %s); set metrics[].aggregation (max|min|sum|any) or narrow the query",
		query, len(samples), labels)
}

func formatLabels(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%q", k, m[k]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// parseSample extracts the numeric value (and labels, for vectors) from a
// scalar or vector result item.
// scalar: [<ts>, "value"]   vector item: {metric:{...}, value:[<ts>, "value"]}
func parseSample(resultType string, raw json.RawMessage) (SeriesSample, error) {
	if resultType == "scalar" {
		var pair []any
		if err := json.Unmarshal(raw, &pair); err != nil || len(pair) != 2 {
			return SeriesSample{}, fmt.Errorf("analysis: bad scalar result")
		}
		v, err := strconv.ParseFloat(fmt.Sprint(pair[1]), 64)
		if err != nil {
			return SeriesSample{}, err
		}
		return SeriesSample{Value: v}, nil
	}
	var item struct {
		Metric map[string]string `json:"metric"`
		Value  []any             `json:"value"`
	}
	if err := json.Unmarshal(raw, &item); err != nil || len(item.Value) != 2 {
		return SeriesSample{}, fmt.Errorf("analysis: bad vector result")
	}
	v, err := strconv.ParseFloat(fmt.Sprint(item.Value[1]), 64)
	if err != nil {
		return SeriesSample{}, err
	}
	return SeriesSample{Labels: item.Metric, Value: v}, nil
}
