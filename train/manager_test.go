package train

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testClient  = "tester"
	waitTimeout = 5 * time.Second
)

type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(e string) {
	l.mu.Lock()
	l.events = append(l.events, e)
	l.mu.Unlock()
}

func (l *eventLog) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.events)
}

type runResult struct {
	err         error
	writeOutput bool
}

type runCall struct {
	spec   RunSpec
	photos int
}

type fakeRunner struct {
	log       *eventLog
	started   chan runCall
	finish    chan runResult
	shutdown  chan struct{}
	once      sync.Once
	ignoreCtx bool
	step      int
	steps     int

	mu        sync.Mutex
	calls     int
	active    int
	maxActive int
	cancelled int
}

func newFakeRunner(log *eventLog) *fakeRunner {
	return &fakeRunner{
		log:      log,
		started:  make(chan runCall, 64),
		finish:   make(chan runResult),
		shutdown: make(chan struct{}),
	}
}

func (f *fakeRunner) Run(ctx context.Context, spec RunSpec, progress func(step, steps int)) error {
	f.mu.Lock()
	f.calls++
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()
	f.log.add("run")
	if f.steps > 0 {
		progress(f.step, f.steps)
	}
	f.started <- runCall{spec: spec, photos: countImages(spec.DatasetDir)}
	done := ctx.Done()
	if f.ignoreCtx {
		done = nil
	}
	select {
	case <-done:
		f.mu.Lock()
		f.cancelled++
		f.mu.Unlock()
		return ctx.Err()
	case <-f.shutdown:
		return errors.New("test shutdown")
	case r := <-f.finish:
		if r.writeOutput {
			if err := os.MkdirAll(filepath.Dir(spec.OutputPath), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(spec.OutputPath, []byte("lora-weights"), 0o644); err != nil {
				return err
			}
		}
		return r.err
	}
}

func (f *fakeRunner) stop() {
	f.once.Do(func() { close(f.shutdown) })
}

func (f *fakeRunner) stats() (calls, maxActive, cancelled int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.maxActive, f.cancelled
}

type fakeLeaser struct {
	log *eventLog
	err error
}

func (l *fakeLeaser) lease(ctx context.Context, client string) (func(), error) {
	l.log.add("lease")
	if l.err != nil {
		return nil, l.err
	}
	return func() { l.log.add("release") }, nil
}

type env struct {
	t      *testing.T
	cfg    Config
	m      *Manager
	runner *fakeRunner
	leaser *fakeLeaser
	log    *eventLog
}

func testConfig(t *testing.T) Config {
	t.Helper()
	dir := t.TempDir()
	python := filepath.Join(dir, "python")
	require.NoError(t, os.WriteFile(python, []byte("#!/bin/sh\n"), 0o755))
	toolkit := filepath.Join(dir, "ai-toolkit")
	require.NoError(t, os.MkdirAll(toolkit, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(toolkit, "run.py"), []byte("print('x')\n"), 0o644))
	jobs := filepath.Join(dir, "trainjobs")
	loras := filepath.Join(dir, "loras")
	require.NoError(t, os.MkdirAll(jobs, 0o755))
	require.NoError(t, os.MkdirAll(loras, 0o755))
	return Config{Python: python, ToolkitDir: toolkit, JobsDir: jobs, LoraDir: loras, Steps: 10}
}

func startEnv(t *testing.T, cfg Config, configure func(r *fakeRunner, l *fakeLeaser)) *env {
	t.Helper()
	log := &eventLog{}
	r := newFakeRunner(log)
	l := &fakeLeaser{log: log}
	if configure != nil {
		configure(r, l)
	}
	m, err := New(cfg, r, l.lease)
	require.NoError(t, err)
	t.Cleanup(m.Close)
	t.Cleanup(r.stop)
	return &env{t: t, cfg: cfg, m: m, runner: r, leaser: l, log: log}
}

func newEnv(t *testing.T) *env {
	t.Helper()
	return startEnv(t, testConfig(t), nil)
}

type upload struct {
	filename    string
	contentType string
	data        []byte
}

func pngBytes(t *testing.T, seed int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for x := 0; x < 8; x++ {
		for y := 0; y < 8; y++ {
			img.Set(x, y, color.RGBA{R: uint8(seed*31 + x), G: uint8(seed*17 + y), B: uint8(seed), A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func jpegBytes(t *testing.T, seed int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for x := 0; x < 8; x++ {
		for y := 0; y < 8; y++ {
			img.Set(x, y, color.RGBA{R: uint8(seed), G: uint8(seed*13 + x), B: uint8(seed*7 + y), A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))
	return buf.Bytes()
}

func photos(t *testing.T, n int) []upload {
	t.Helper()
	out := make([]upload, 0, n)
	for i := 0; i < n; i++ {
		if i%2 == 0 {
			out = append(out, upload{filename: fmt.Sprintf("p%02d.png", i), contentType: "image/png", data: pngBytes(t, i)})
		} else {
			out = append(out, upload{filename: fmt.Sprintf("p%02d.jpg", i), contentType: "image/jpeg", data: jpegBytes(t, i)})
		}
	}
	return out
}

func personFields(name, gender string) map[string]string {
	return map[string]string{"name": name, "gender": gender, "client": testClient}
}

func multipartBody(t *testing.T, fields map[string]string, files []upload) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		require.NoError(t, w.WriteField(k, v))
	}
	for _, f := range files {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="photos"; filename=%q`, f.filename))
		h.Set("Content-Type", f.contentType)
		p, err := w.CreatePart(h)
		require.NoError(t, err)
		_, err = p.Write(f.data)
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return &buf, w.FormDataContentType()
}

func (e *env) do(method, target string, body io.Reader, contentType string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, target, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	e.m.ServeHTTP(rec, req)
	return rec
}

func (e *env) post(fields map[string]string, files []upload) *httptest.ResponseRecorder {
	e.t.Helper()
	body, ct := multipartBody(e.t, fields, files)
	return e.do(http.MethodPost, "/api/train/jobs", body, ct)
}

func (e *env) get(target string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(http.MethodGet, target, nil, "")
}

func (e *env) submit(name, gender string) jobView {
	e.t.Helper()
	rec := e.post(personFields(name, gender), photos(e.t, 10))
	require.Equal(e.t, http.StatusAccepted, rec.Code, rec.Body.String())
	return decodeJob(e.t, rec)
}

func (e *env) cancel(id string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(http.MethodPost, "/api/train/jobs/"+id+"/cancel", nil, "")
}

func (e *env) waitStarted() runCall {
	e.t.Helper()
	select {
	case c := <-e.runner.started:
		return c
	case <-time.After(waitTimeout):
		e.t.Fatal("runner was not started")
		return runCall{}
	}
}

func (e *env) finishRun(r runResult) {
	e.t.Helper()
	select {
	case e.runner.finish <- r:
	case <-time.After(waitTimeout):
		e.t.Fatal("runner is not waiting for a result")
	}
}

func (e *env) job(id string) jobView {
	e.t.Helper()
	rec := e.get("/api/train/jobs/" + id)
	require.Equal(e.t, http.StatusOK, rec.Code, rec.Body.String())
	return decodeJob(e.t, rec)
}

func (e *env) waitJob(id, desc string, cond func(jobView) bool) jobView {
	e.t.Helper()
	deadline := time.Now().Add(waitTimeout)
	var last jobView
	for time.Now().Before(deadline) {
		rec := e.get("/api/train/jobs/" + id)
		if rec.Code == http.StatusOK {
			last = decodeJob(e.t, rec)
			if cond(last) {
				return last
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatalf("job %s: timeout waiting for %s, last: %+v", id, desc, last)
	return last
}

func (e *env) waitStatus(id string, want ...string) jobView {
	e.t.Helper()
	return e.waitJob(id, "status "+strings.Join(want, "|"), func(j jobView) bool {
		return slices.Contains(want, j.Status)
	})
}

func (e *env) people() []personView {
	e.t.Helper()
	rec := e.get("/api/train/people")
	require.Equal(e.t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		People []personView `json:"people"`
	}
	require.NoError(e.t, json.NewDecoder(rec.Body).Decode(&resp))
	return resp.People
}

func (e *env) jobsOf(client string) []jobView {
	e.t.Helper()
	rec := e.get("/api/train/jobs?client=" + client)
	require.Equal(e.t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Jobs []jobView `json:"jobs"`
	}
	require.NoError(e.t, json.NewDecoder(rec.Body).Decode(&resp))
	return resp.Jobs
}

func (e *env) loraPath(name string) string {
	return filepath.Join(e.cfg.LoraDir, name+".safetensors")
}

type progressView struct {
	Step  int `json:"step"`
	Steps int `json:"steps"`
}

type errorView struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type jobView struct {
	RawID     json.RawMessage `json:"id"`
	Client    string          `json:"client"`
	ClientRef string          `json:"client_ref"`
	Name      string          `json:"name"`
	Trigger   string          `json:"trigger"`
	Gender    string          `json:"gender"`
	Status    string          `json:"status"`
	Progress  progressView    `json:"progress"`
	Error     *errorView      `json:"error"`
}

func (j jobView) ID() string {
	var s string
	if err := json.Unmarshal(j.RawID, &s); err == nil {
		return s
	}
	return string(j.RawID)
}

type personView struct {
	Name    string `json:"name"`
	Trigger string `json:"trigger"`
	Gender  string `json:"gender"`
	Base    string `json:"base"`
	Lora    string `json:"lora"`
	Steps   int    `json:"steps"`
}

func decodeJob(t *testing.T, rec *httptest.ResponseRecorder) jobView {
	t.Helper()
	var j jobView
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &j), rec.Body.String())
	require.NotEmpty(t, j.ID(), rec.Body.String())
	return j
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var resp struct {
		Error errorView `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), rec.Body.String())
	assert.NotEmpty(t, resp.Error.Message, rec.Body.String())
	return resp.Error.Code
}

func isImageFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	return strings.HasPrefix(http.DetectContentType(head[:n]), "image/")
}

func countImages(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return -1
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && isImageFile(filepath.Join(dir, e.Name())) {
			n++
		}
	}
	return n
}

func imagesUnder(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && isImageFile(path) {
			found = append(found, path)
		}
		return nil
	})
	require.NoError(t, err)
	return found
}

func assertGone(t *testing.T, path string) {
	t.Helper()
	assert.Eventually(t, func() bool {
		_, err := os.Stat(path)
		return errors.Is(err, os.ErrNotExist)
	}, waitTimeout, 10*time.Millisecond, "%s must be removed", path)
}

func TestUpload_ValidPhotosTrainsPerson(t *testing.T) {
	e := newEnv(t)

	j := e.submit("anna", "woman")
	assert.Contains(t, []string{"queued", "running"}, j.Status)
	assert.Equal(t, "anna", j.Name)
	assert.Equal(t, "woman", j.Gender)
	assert.Equal(t, "anna_woman", j.Trigger)
	assert.Equal(t, testClient, j.Client)
	assert.Nil(t, j.Error)

	call := e.waitStarted()
	assert.Equal(t, 10, call.photos)
	assert.Equal(t, 10, call.spec.Steps)
	e.finishRun(runResult{writeOutput: true})

	done := e.waitStatus(j.ID(), "completed")
	assert.Nil(t, done.Error)
	e.m.Wait()

	assert.FileExists(t, e.loraPath("anna"))
	people := e.people()
	require.Len(t, people, 1)
	assert.Equal(t, personView{Name: "anna", Trigger: "anna_woman", Gender: "woman", Base: "Z-Image", Lora: "anna", Steps: 10}, people[0])
}

func TestUpload_NameIsLowercased(t *testing.T) {
	e := newEnv(t)

	rec := e.post(personFields("Anna", "woman"), photos(t, 10))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	j := decodeJob(t, rec)
	assert.Equal(t, "anna", j.Name)
	assert.Equal(t, "anna_woman", j.Trigger)
}

func TestUpload_ZeroStepsDefaultsTo2000(t *testing.T) {
	cfg := testConfig(t)
	cfg.Steps = 0
	e := startEnv(t, cfg, nil)

	e.submit("anna", "woman")
	call := e.waitStarted()
	assert.Equal(t, 2000, call.spec.Steps)
}

func TestUpload_InvalidRequest(t *testing.T) {
	notImage := upload{filename: "notes.jpg", contentType: "image/jpeg", data: []byte("just some text, not a picture at all")}
	tests := []struct {
		name   string
		fields map[string]string
		files  func(t *testing.T) []upload
	}{
		{"nine photos", personFields("anna", "woman"), func(t *testing.T) []upload { return photos(t, 9) }},
		{"fifty one photos", personFields("anna", "woman"), func(t *testing.T) []upload { return photos(t, 51) }},
		{"text file as jpg", personFields("anna", "woman"), func(t *testing.T) []upload { return append(photos(t, 10), notImage) }},
		{"text file instead of photo", personFields("anna", "woman"), func(t *testing.T) []upload { return append(photos(t, 9), notImage) }},
		{"empty name", personFields("", "woman"), func(t *testing.T) []upload { return photos(t, 10) }},
		{"name of special chars", personFields("!!!@@##", "woman"), func(t *testing.T) []upload { return photos(t, 10) }},
		{"name too short", personFields("a", "woman"), func(t *testing.T) []upload { return photos(t, 10) }},
		{"name too long", personFields(strings.Repeat("a", 33), "woman"), func(t *testing.T) []upload { return photos(t, 10) }},
		{"missing name", map[string]string{"gender": "woman", "client": testClient}, func(t *testing.T) []upload { return photos(t, 10) }},
		{"gender other", personFields("anna", "other"), func(t *testing.T) []upload { return photos(t, 10) }},
		{"missing gender", map[string]string{"name": "anna", "client": testClient}, func(t *testing.T) []upload { return photos(t, 10) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)

			rec := e.post(tt.fields, tt.files(t))
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Equal(t, "invalid_request", errorCode(t, rec))
			assert.Empty(t, e.jobsOf(testClient))
			calls, _, _ := e.runner.stats()
			assert.Zero(t, calls)
			assert.Empty(t, imagesUnder(t, e.cfg.JobsDir))
		})
	}
}

func TestUpload_ValidNameBounds(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"two chars", "ab"},
		{"thirty two chars", strings.Repeat("b", 32)},
		{"digits and underscore", "anna_2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)

			rec := e.post(personFields(tt.in, "person"), photos(t, 10))
			require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
			assert.Equal(t, tt.in, decodeJob(t, rec).Name)
		})
	}
}

func TestUpload_FileOver30MBTooLarge(t *testing.T) {
	e := newEnv(t)

	big := append(pngBytes(t, 99), make([]byte, 31<<20)...)
	files := append(photos(t, 9), upload{filename: "big.png", contentType: "image/png", data: big})

	rec := e.post(personFields("anna", "woman"), files)
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
	assert.Equal(t, "too_large", errorCode(t, rec))
	assert.Empty(t, e.jobsOf(testClient))
	assert.Empty(t, imagesUnder(t, e.cfg.JobsDir))
}

func TestUpload_NameTaken(t *testing.T) {
	t.Run("lora file exists", func(t *testing.T) {
		e := newEnv(t)
		require.NoError(t, os.WriteFile(e.loraPath("anna"), []byte("existing"), 0o644))

		rec := e.post(personFields("anna", "woman"), photos(t, 10))
		require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		assert.Equal(t, "name_taken", errorCode(t, rec))
		data, err := os.ReadFile(e.loraPath("anna"))
		require.NoError(t, err)
		assert.Equal(t, "existing", string(data))
	})

	t.Run("active job with the name", func(t *testing.T) {
		e := newEnv(t)
		e.submit("anna", "woman")
		e.waitStarted()

		fields := personFields("anna", "man")
		fields["client"] = "other"
		rec := e.post(fields, photos(t, 10))
		require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		assert.Equal(t, "name_taken", errorCode(t, rec))
		assert.Empty(t, e.jobsOf("other"))
	})

	t.Run("trained person exists", func(t *testing.T) {
		e := newEnv(t)
		j := e.submit("anna", "woman")
		e.waitStarted()
		e.finishRun(runResult{writeOutput: true})
		e.waitStatus(j.ID(), "completed")

		rec := e.post(personFields("anna", "woman"), photos(t, 10))
		require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		assert.Equal(t, "name_taken", errorCode(t, rec))
	})
}

func TestTrainerAvailability(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(t *testing.T, cfg *Config)
		available bool
	}{
		{"installed", func(t *testing.T, cfg *Config) {}, true},
		{"python missing", func(t *testing.T, cfg *Config) {
			cfg.Python = filepath.Join(t.TempDir(), "no-python")
		}, false},
		{"run.py missing", func(t *testing.T, cfg *Config) {
			require.NoError(t, os.Remove(filepath.Join(cfg.ToolkitDir, "run.py")))
		}, false},
		{"toolkit dir missing", func(t *testing.T, cfg *Config) {
			cfg.ToolkitDir = filepath.Join(t.TempDir(), "no-toolkit")
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			tt.mutate(t, &cfg)
			e := startEnv(t, cfg, nil)

			assert.Equal(t, tt.available, e.m.Available())
			rec := e.post(personFields("anna", "woman"), photos(t, 10))
			if tt.available {
				assert.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
				return
			}
			require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
			assert.Equal(t, "not_available", errorCode(t, rec))
			calls, _, _ := e.runner.stats()
			assert.Zero(t, calls)
			assert.Empty(t, imagesUnder(t, cfg.JobsDir))
		})
	}
}

func TestJob_ProgressVisible(t *testing.T) {
	e := startEnv(t, testConfig(t), func(r *fakeRunner, l *fakeLeaser) {
		r.step, r.steps = 5, 10
	})

	j := e.submit("anna", "woman")
	e.waitStarted()

	got := e.waitJob(j.ID(), "running 5/10", func(j jobView) bool {
		return j.Status == "running" && j.Progress == progressView{Step: 5, Steps: 10}
	})
	assert.Nil(t, got.Error)
}

func TestJob_Cancel(t *testing.T) {
	e := newEnv(t)
	j := e.submit("anna", "woman")
	e.waitStarted()

	rec := e.cancel(j.ID())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, j.ID(), decodeJob(t, rec).ID())

	e.waitStatus(j.ID(), "cancelled")
	e.m.Wait()
	_, _, cancelled := e.runner.stats()
	assert.Equal(t, 1, cancelled)
	assert.NoFileExists(t, e.loraPath("anna"))
	assert.Empty(t, e.people())
}

func TestJob_CancelQueued(t *testing.T) {
	e := newEnv(t)
	first := e.submit("anna", "woman")
	e.waitStarted()
	second := e.submit("boris", "man")
	e.waitStatus(second.ID(), "queued")

	rec := e.cancel(second.ID())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	e.waitStatus(second.ID(), "cancelled")

	e.finishRun(runResult{writeOutput: true})
	e.waitStatus(first.ID(), "completed")
	e.m.Wait()
	calls, _, _ := e.runner.stats()
	assert.Equal(t, 1, calls)
	assert.NoFileExists(t, e.loraPath("boris"))
}

func TestJob_NotFound(t *testing.T) {
	e := newEnv(t)

	rec := e.get("/api/train/jobs/missing")
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Equal(t, "not_found", errorCode(t, rec))

	rec = e.cancel("missing")
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

func TestJobs_ListByClient(t *testing.T) {
	e := newEnv(t)
	e.submit("anna", "woman")
	e.waitStarted()
	fields := personFields("boris", "man")
	fields["client"] = "other"
	rec := e.post(fields, photos(t, 10))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

	mine := e.jobsOf(testClient)
	require.Len(t, mine, 1)
	assert.Equal(t, "anna", mine[0].Name)
	theirs := e.jobsOf("other")
	require.Len(t, theirs, 1)
	assert.Equal(t, "boris", theirs[0].Name)
	assert.Empty(t, e.jobsOf("nobody"))
}

func TestJob_DatasetRemovedAfterFinish(t *testing.T) {
	tests := []struct {
		name   string
		end    func(e *env, id string)
		status string
	}{
		{"completed", func(e *env, id string) { e.finishRun(runResult{writeOutput: true}) }, "completed"},
		{"failed", func(e *env, id string) { e.finishRun(runResult{err: errors.New("cuda out of memory")}) }, "failed"},
		{"cancelled", func(e *env, id string) {
			require.Equal(e.t, http.StatusOK, e.cancel(id).Code)
		}, "cancelled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			j := e.submit("anna", "woman")
			call := e.waitStarted()
			require.Equal(t, 10, call.photos)

			tt.end(e, j.ID())
			e.waitStatus(j.ID(), tt.status)
			e.m.Wait()

			assertGone(t, call.spec.DatasetDir)
			assert.Empty(t, imagesUnder(t, e.cfg.JobsDir))
		})
	}
}

func TestJob_RunnerErrorFails(t *testing.T) {
	e := newEnv(t)
	j := e.submit("anna", "woman")
	e.waitStarted()

	e.finishRun(runResult{err: errors.New("cuda out of memory")})
	got := e.waitStatus(j.ID(), "failed")
	require.NotNil(t, got.Error)
	assert.NotEmpty(t, got.Error.Code)
	e.m.Wait()
	assert.NoFileExists(t, e.loraPath("anna"))
	assert.Empty(t, e.people())
}

func TestJob_MissingOutputFails(t *testing.T) {
	e := newEnv(t)
	j := e.submit("anna", "woman")
	e.waitStarted()

	e.finishRun(runResult{})
	got := e.waitStatus(j.ID(), "failed")
	assert.NotNil(t, got.Error)
	e.m.Wait()
	assert.NoFileExists(t, e.loraPath("anna"))
	assert.Empty(t, e.people())
}

func TestJob_LeaseAroundRun(t *testing.T) {
	tests := []struct {
		name   string
		end    func(e *env, id string)
		status string
	}{
		{"completed", func(e *env, id string) { e.finishRun(runResult{writeOutput: true}) }, "completed"},
		{"runner error", func(e *env, id string) { e.finishRun(runResult{err: errors.New("boom")}) }, "failed"},
		{"cancelled", func(e *env, id string) {
			require.Equal(e.t, http.StatusOK, e.cancel(id).Code)
		}, "cancelled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			j := e.submit("anna", "woman")
			e.waitStarted()
			assert.Equal(t, []string{"lease", "run"}, e.log.list())

			tt.end(e, j.ID())
			e.waitStatus(j.ID(), tt.status)
			e.m.Wait()

			assert.Eventually(t, func() bool {
				return slices.Equal([]string{"lease", "run", "release"}, e.log.list())
			}, waitTimeout, 10*time.Millisecond, "events: %v", e.log.list())
		})
	}
}

func TestJob_LeaseErrorFailsWithoutRun(t *testing.T) {
	e := startEnv(t, testConfig(t), func(r *fakeRunner, l *fakeLeaser) {
		l.err = errors.New("gpu queue full")
	})

	j := e.submit("anna", "woman")
	got := e.waitStatus(j.ID(), "failed")
	assert.NotNil(t, got.Error)
	e.m.Wait()

	calls, _, _ := e.runner.stats()
	assert.Zero(t, calls)
	assert.Equal(t, []string{"lease"}, e.log.list())
	assert.Empty(t, imagesUnder(t, e.cfg.JobsDir))
}

func TestJob_RunsOneAtATime(t *testing.T) {
	e := newEnv(t)
	first := e.submit("anna", "woman")
	e.waitStarted()
	second := e.submit("boris", "man")

	e.waitStatus(second.ID(), "queued")
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		assert.Equal(t, "queued", e.job(second.ID()).Status)
		time.Sleep(20 * time.Millisecond)
	}
	calls, _, _ := e.runner.stats()
	assert.Equal(t, 1, calls)

	e.finishRun(runResult{writeOutput: true})
	e.waitStatus(first.ID(), "completed")
	e.waitStarted()
	e.finishRun(runResult{writeOutput: true})
	e.waitStatus(second.ID(), "completed")
	e.m.Wait()

	calls, maxActive, _ := e.runner.stats()
	assert.Equal(t, 2, calls)
	assert.Equal(t, 1, maxActive)
	assert.FileExists(t, e.loraPath("anna"))
	assert.FileExists(t, e.loraPath("boris"))
}

func TestNew_RestoresJobsAfterRestart(t *testing.T) {
	cfg := testConfig(t)
	before := startEnv(t, cfg, func(r *fakeRunner, l *fakeLeaser) {
		r.ignoreCtx = true
	})
	done := before.submit("anna", "woman")
	before.waitStarted()
	before.finishRun(runResult{writeOutput: true})
	before.waitStatus(done.ID(), "completed")

	unfinished := before.submit("boris", "man")
	call := before.waitStarted()
	require.Equal(t, 10, call.photos)

	after := startEnv(t, cfg, nil)

	lost := after.job(unfinished.ID())
	assert.Equal(t, "lost", lost.Status)
	assert.Equal(t, "boris", lost.Name)
	assert.Equal(t, "completed", after.job(done.ID()).Status)
	assertGone(t, call.spec.DatasetDir)
	assert.Empty(t, imagesUnder(t, cfg.JobsDir))

	names := []string{}
	for _, j := range after.jobsOf(testClient) {
		names = append(names, j.Name)
	}
	assert.ElementsMatch(t, []string{"anna", "boris"}, names)
	people := after.people()
	require.Len(t, people, 1)
	assert.Equal(t, "anna", people[0].Name)

	calls, _, _ := after.runner.stats()
	assert.Zero(t, calls)
}

func TestUpload_SameClientRefReturnsSameJob(t *testing.T) {
	e := newEnv(t)
	fields := personFields("anna", "woman")
	fields["client_ref"] = "ref-1"

	rec := e.post(fields, photos(t, 10))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	first := decodeJob(t, rec)
	assert.Equal(t, "ref-1", first.ClientRef)
	e.waitStarted()

	rec = e.post(fields, photos(t, 10))
	require.Contains(t, []int{http.StatusOK, http.StatusAccepted}, rec.Code, rec.Body.String())
	assert.Equal(t, first.ID(), decodeJob(t, rec).ID())
	assert.Len(t, e.jobsOf(testClient), 1)

	e.finishRun(runResult{writeOutput: true})
	e.waitStatus(first.ID(), "completed")
	e.m.Wait()
	calls, _, _ := e.runner.stats()
	assert.Equal(t, 1, calls)
}

func TestPeople_Delete(t *testing.T) {
	e := newEnv(t)
	j := e.submit("anna", "woman")
	e.waitStarted()
	e.finishRun(runResult{writeOutput: true})
	e.waitStatus(j.ID(), "completed")
	e.m.Wait()
	require.FileExists(t, e.loraPath("anna"))

	rec := e.do(http.MethodDelete, "/api/train/people/anna", nil, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.NoFileExists(t, e.loraPath("anna"))
	assert.Empty(t, e.people())
	entries, err := os.ReadDir(e.cfg.LoraDir)
	require.NoError(t, err)
	for _, en := range entries {
		assert.False(t, strings.HasPrefix(en.Name(), "anna."), "leftover person data: %s", en.Name())
	}

	rec = e.do(http.MethodDelete, "/api/train/people/anna", nil, "")
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

	again := e.post(personFields("anna", "woman"), photos(t, 10))
	assert.Equal(t, http.StatusAccepted, again.Code, again.Body.String())
}

func TestPeople_DeleteUnknown(t *testing.T) {
	e := newEnv(t)

	rec := e.do(http.MethodDelete, "/api/train/people/nobody", nil, "")
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Equal(t, "not_found", errorCode(t, rec))
}

func TestPeople_DeleteBusy(t *testing.T) {
	e := newEnv(t)
	j := e.submit("anna", "woman")
	e.waitStarted()

	rec := e.do(http.MethodDelete, "/api/train/people/anna", nil, "")
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, "busy", errorCode(t, rec))
	assert.Equal(t, "running", e.job(j.ID()).Status)
}
