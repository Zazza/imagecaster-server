package dl

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"
)

var downloadStall = 60 * time.Second

type stallTimer interface {
	Reset(d time.Duration) bool
	Stop() bool
}

var newStallTimer = func(d time.Duration, f func()) stallTimer {
	return time.AfterFunc(d, f)
}

var downloadClient = &http.Client{Transport: newDownloadTransport()}

func newDownloadTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	t.TLSHandshakeTimeout = 30 * time.Second
	t.ResponseHeaderTimeout = 60 * time.Second
	return t
}

var ErrStalled = errors.New("download stalled: no data received")

func Get(parent context.Context, url string) (*http.Response, error) {
	return GetFrom(parent, url, 0, Options{})
}

func GetFrom(parent context.Context, url string, offset int64, o Options) (*http.Response, error) {
	client := o.Client
	if client == nil {
		client = downloadClient
	}
	stall := o.Stall
	if stall <= 0 {
		stall = downloadStall
	}
	ctx, cancel := context.WithCancelCause(parent)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cancel(nil)
		return nil, err
	}
	for k, v := range o.Header {
		req.Header[k] = v
	}
	if offset > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
	}
	resp, err := client.Do(req)
	if err != nil {
		cancel(nil)
		return nil, err
	}
	timer := newStallTimer(stall, func() { cancel(ErrStalled) })
	resp.Body = &stallBody{body: resp.Body, ctx: ctx, timer: timer, cancel: cancel, stall: stall}
	return resp, nil
}

type stallBody struct {
	body interface {
		Read([]byte) (int, error)
		Close() error
	}
	ctx    context.Context
	timer  stallTimer
	cancel context.CancelCauseFunc
	stall  time.Duration
}

func (s *stallBody) Read(p []byte) (int, error) {
	n, err := s.body.Read(p)
	if n > 0 {
		s.timer.Reset(s.stall)
	}
	if err != nil && context.Cause(s.ctx) == ErrStalled {
		return n, ErrStalled
	}
	return n, err
}

func (s *stallBody) Close() error {
	s.timer.Stop()
	s.cancel(nil)
	return s.body.Close()
}
