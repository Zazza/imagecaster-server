package probe

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

var client = &http.Client{}

type StatusError struct {
	Code int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("status %d", e.Code)
}

func Probe(ctx context.Context, url string) (time.Duration, error) {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		return latency, err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return latency, &StatusError{Code: resp.StatusCode}
	}
	return latency, nil
}

func ProbeTimeout(url string, timeout time.Duration) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return Probe(ctx, url)
}
