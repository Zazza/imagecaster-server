package main

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type leaseLog struct {
	mu     sync.Mutex
	events []string
}

func (l *leaseLog) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, s)
}

func (l *leaseLog) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

type recordingUnloader struct {
	log *leaseLog
	err error
}

func (u *recordingUnloader) UnloadIdle(context.Context) error {
	u.log.add("llm")
	return u.err
}

func fakeLease(log *leaseLog, err error) func(context.Context, string, func()) (func(), error) {
	return func(_ context.Context, client string, _ func()) (func(), error) {
		log.add("lease:" + client)
		if err != nil {
			return nil, err
		}
		return func() { log.add("release") }, nil
	}
}

func fakeUnloadSD(log *leaseLog, err error) func(context.Context) error {
	return func(context.Context) error {
		log.add("sd")
		return err
	}
}

func TestTrainLeaser(t *testing.T) {
	leaseErr := errors.New("gate closed")
	tests := []struct {
		name     string
		leaseErr error
		sdErr    error
		llm      bool
		llmErr   error
		wantErr  error
		want     []string
	}{
		{"unloads sd and idle llm", nil, nil, true, nil, nil, []string{"lease:c1", "sd", "llm", "release"}},
		{"no llm", nil, nil, false, nil, nil, []string{"lease:c1", "sd", "release"}},
		{"llm unload error ignored", nil, nil, true, errors.New("busy"), nil, []string{"lease:c1", "sd", "llm", "release"}},
		{"sd unload error ignored", nil, errors.New("sd down"), true, nil, nil, []string{"lease:c1", "sd", "llm", "release"}},
		{"lease error unloads nothing", leaseErr, nil, true, nil, leaseErr, []string{"lease:c1"}},
		{"lease error without llm", leaseErr, nil, false, nil, leaseErr, []string{"lease:c1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := &leaseLog{}
			var llm idleUnloader
			if tt.llm {
				llm = &recordingUnloader{log: log, err: tt.llmErr}
			}
			leaser := trainLeaser(fakeLease(log, tt.leaseErr), fakeUnloadSD(log, tt.sdErr), llm, func() {})
			require.NotNil(t, leaser)
			release, err := leaser(context.Background(), "c1")
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, release)
				assert.Equal(t, tt.want, log.list())
				return
			}
			require.NoError(t, err)
			require.NotNil(t, release)
			release()
			got := log.list()
			require.NotEmpty(t, got)
			assert.Equal(t, "lease:c1", got[0], "lease is taken before unloading")
			assert.Equal(t, "release", got[len(got)-1])
			assert.ElementsMatch(t, tt.want, got)
		})
	}
}

func TestTrainLeaserPassesOnLost(t *testing.T) {
	lost := 0
	var got func()
	lease := func(_ context.Context, _ string, onLost func()) (func(), error) {
		got = onLost
		return func() {}, nil
	}
	leaser := trainLeaser(lease, func(context.Context) error { return nil }, nil, func() { lost++ })
	_, err := leaser(context.Background(), "c1")
	require.NoError(t, err)
	require.NotNil(t, got)
	got()
	assert.Equal(t, 1, lost)
}
