package installer

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"
)

var downloadStall = 60 * time.Second

var downloadClient = &http.Client{Transport: newDownloadTransport()}

func newDownloadTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	t.TLSHandshakeTimeout = 30 * time.Second
	t.ResponseHeaderTimeout = 60 * time.Second
	return t
}

var errStalled = errors.New("download stalled: no data received")

func HTTPGet(url string) (*http.Response, error) {
	return HTTPGetFrom(url, 0)
}

func HTTPGetFrom(url string, offset int64) (*http.Response, error) {
	ctx, cancel := context.WithCancelCause(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cancel(nil)
		return nil, err
	}
	if offset > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
	}
	resp, err := downloadClient.Do(req)
	if err != nil {
		cancel(nil)
		return nil, err
	}
	timer := time.AfterFunc(downloadStall, func() { cancel(errStalled) })
	resp.Body = &stallBody{body: resp.Body, ctx: ctx, timer: timer, cancel: cancel}
	return resp, nil
}

type stallBody struct {
	body interface {
		Read([]byte) (int, error)
		Close() error
	}
	ctx    context.Context
	timer  *time.Timer
	cancel context.CancelCauseFunc
}

func (s *stallBody) Read(p []byte) (int, error) {
	n, err := s.body.Read(p)
	if n > 0 {
		s.timer.Reset(downloadStall)
	}
	if err != nil && context.Cause(s.ctx) == errStalled {
		return n, errStalled
	}
	return n, err
}

func (s *stallBody) Close() error {
	s.timer.Stop()
	s.cancel(nil)
	return s.body.Close()
}
