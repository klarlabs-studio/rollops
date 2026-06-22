package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// httpDoer is the slice of http.Client the provider needs (injectable in tests).
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Prometheus is a MetricsProvider backed by the Prometheus HTTP query API. It
// reads the first scalar/vector sample of an instant query. Other backends
// (Datadog, CloudWatch, a custom metrics service) implement MetricsProvider the
// same way — Prometheus is just the first concrete one.
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

// Query runs an instant query and returns the first sample's value.
func (p Prometheus) Query(ctx context.Context, query string) (float64, error) {
	u := fmt.Sprintf("%s/api/v1/query?query=%s", p.Addr, url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return 0, fmt.Errorf("analysis: prometheus: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("analysis: prometheus status %d", resp.StatusCode)
	}

	var pr promResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return 0, fmt.Errorf("analysis: prometheus decode: %w", err)
	}
	if pr.Status != "success" {
		return 0, fmt.Errorf("analysis: prometheus error: %s", pr.Error)
	}
	if len(pr.Data.Result) == 0 {
		return 0, fmt.Errorf("analysis: prometheus query %q returned no data", query)
	}
	return parseSample(pr.Data.ResultType, pr.Data.Result[0])
}

// parseSample extracts the numeric value from a scalar or vector result.
// scalar: [<ts>, "value"]   vector item: {metric:{...}, value:[<ts>, "value"]}
func parseSample(resultType string, raw json.RawMessage) (float64, error) {
	if resultType == "scalar" {
		var pair []any
		if err := json.Unmarshal(raw, &pair); err != nil || len(pair) != 2 {
			return 0, fmt.Errorf("analysis: bad scalar result")
		}
		return strconv.ParseFloat(fmt.Sprint(pair[1]), 64)
	}
	var item struct {
		Value []any `json:"value"`
	}
	if err := json.Unmarshal(raw, &item); err != nil || len(item.Value) != 2 {
		return 0, fmt.Errorf("analysis: bad vector result")
	}
	return strconv.ParseFloat(fmt.Sprint(item.Value[1]), 64)
}
