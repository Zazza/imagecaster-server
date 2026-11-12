package llamacpp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fixtureLayer struct {
	media string
	data  []byte
}

func digestOf(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

func putBlob(t *testing.T, root string, b []byte) Layer {
	t.Helper()
	d := digestOf(b)
	p := filepath.Join(root, "blobs", "sha256-"+strings.TrimPrefix(d, "sha256:"))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, b, 0o644))
	return Layer{Digest: d, Size: int64(len(b))}
}

func manifestFor(t *testing.T, root, family string, layers ...fixtureLayer) []byte {
	t.Helper()
	cfg := putBlob(t, root, []byte(`{"model_format":"gguf","model_family":"`+family+`","model_families":["`+family+`"],"model_type":"8B","file_type":"Q4_K_M"}`))
	cfg.MediaType = "application/vnd.docker.container.image.v1+json"
	mf := Manifest{SchemaVersion: 2, MediaType: MediaManifest, Config: cfg}
	for _, l := range layers {
		bl := putBlob(t, root, l.data)
		bl.MediaType = l.media
		mf.Layers = append(mf.Layers, bl)
	}
	raw, err := json.Marshal(mf)
	require.NoError(t, err)
	return raw
}

func addModel(t *testing.T, root, name, family string, layers ...fixtureLayer) {
	t.Helper()
	n, err := ParseName(name)
	require.NoError(t, err)
	raw := manifestFor(t, root, family, layers...)
	p := filepath.Join(root, "manifests", n.manifestRel())
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, raw, 0o644))
}

func fixtureStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	addModel(t, root, "huihui_ai/qwen2.5-abliterate:14b", "qwen2",
		fixtureLayer{MediaModel, []byte("GGUF-qwen14b")},
		fixtureLayer{MediaSystem, []byte("You are Qwen.")},
		fixtureLayer{MediaParams, []byte(`{"stop":["<|im_end|>"],"temperature":0.3}`)})
	addModel(t, root, "hf.co/GitMylo/nsfwcaption-qwen3-vl-8b-v3-gguf:Q4_K_M", "qwen3vl",
		fixtureLayer{MediaModel, []byte("GGUF-vl")},
		fixtureLayer{MediaProjector, []byte("GGUF-mmproj")})
	addModel(t, root, "qwen2.5vl:7b", "qwen25vl", fixtureLayer{MediaModel, []byte("GGUF-ollama-own")})
	addModel(t, root, "qwen2.5-chat-ru", "qwen2", fixtureLayer{MediaModel, []byte("GGUF-ru")})
	return &Store{Root: root}
}

func TestParseName(t *testing.T) {
	cases := []struct {
		in, want, rel string
		bad           bool
	}{
		{in: "qwen2.5-chat-ru", want: "qwen2.5-chat-ru:latest", rel: "registry.ollama.ai/library/qwen2.5-chat-ru/latest"},
		{in: "qwen2.5vl:7b", want: "qwen2.5vl:7b", rel: "registry.ollama.ai/library/qwen2.5vl/7b"},
		{in: "huihui_ai/qwen2.5-abliterate:14b", want: "huihui_ai/qwen2.5-abliterate:14b", rel: "registry.ollama.ai/huihui_ai/qwen2.5-abliterate/14b"},
		{in: "hf.co/GitMylo/nsfwcaption-qwen3-vl-8b-v3-gguf:Q4_K_M", want: "hf.co/GitMylo/nsfwcaption-qwen3-vl-8b-v3-gguf:Q4_K_M", rel: "hf.co/GitMylo/nsfwcaption-qwen3-vl-8b-v3-gguf/Q4_K_M"},
		{in: "registry.ollama.ai/library/x:1", want: "x:1", rel: "registry.ollama.ai/library/x/1"},
		{in: "", bad: true},
		{in: "../etc/passwd", bad: true},
		{in: "a/b/c/d", bad: true},
		{in: "a/..:x", bad: true},
		{in: "model:../x", bad: true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			n, err := ParseName(c.in)
			if c.bad {
				assert.ErrorIs(t, err, ErrBadName)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, c.want, n.String())
			assert.Equal(t, filepath.FromSlash(c.rel), n.manifestRel())
		})
	}
}

func TestStoreListAndResolve(t *testing.T) {
	s := fixtureStore(t)
	list, err := s.List()
	require.NoError(t, err)
	var names []string
	for _, m := range list {
		names = append(names, m.Name.String())
	}
	assert.ElementsMatch(t, []string{
		"huihui_ai/qwen2.5-abliterate:14b",
		"hf.co/GitMylo/nsfwcaption-qwen3-vl-8b-v3-gguf:Q4_K_M",
		"qwen2.5vl:7b",
		"qwen2.5-chat-ru:latest",
	}, names)

	vl, err := s.Resolve("hf.co/gitmylo/nsfwcaption-qwen3-vl-8b-v3-gguf:q4_k_m")
	require.NoError(t, err)
	assert.True(t, vl.Vision())
	assert.Equal(t, "qwen3vl", vl.Family())
	assert.FileExists(t, vl.ModelPath)
	assert.FileExists(t, vl.Projector)
	assert.NoError(t, vl.Supported())

	own, err := s.Resolve("qwen2.5vl:7b")
	require.NoError(t, err)
	assert.ErrorIs(t, own.Supported(), ErrUnsupported)

	_, err = s.Resolve("nope")
	assert.ErrorIs(t, err, ErrNotFound)

	q, err := s.Resolve("huihui_ai/qwen2.5-abliterate:14b")
	require.NoError(t, err)
	assert.Equal(t, "You are Qwen.", s.Text(q, MediaSystem))
	assert.Equal(t, 0.3, s.Params(q)["temperature"])
}

func TestWritePreset(t *testing.T) {
	s := fixtureStore(t)
	path := filepath.Join(t.TempDir(), "models.ini")
	ids, err := s.WritePreset(path, PresetOptions{ContextSize: 8192})
	require.NoError(t, err)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	ini := string(raw)

	assert.Contains(t, ini, "[*]\nc = 8192\nnp = 1\n")
	vlID := ids["hf.co/GitMylo/nsfwcaption-qwen3-vl-8b-v3-gguf:Q4_K_M"]
	require.NotEmpty(t, vlID)
	vl, _ := s.Resolve("hf.co/GitMylo/nsfwcaption-qwen3-vl-8b-v3-gguf:Q4_K_M")
	assert.Contains(t, ini, "["+vlID+"]\nmodel = "+vl.ModelPath+"\nmmproj = "+vl.Projector+"\n")
	_, hasOwn := ids["qwen2.5vl:7b"]
	assert.False(t, hasOwn, "Ollama-own format must not reach the router")
	for _, id := range ids {
		assert.Regexp(t, `^[A-Za-z0-9._-]+$`, id)
	}
}

func TestStoreDeleteKeepsSharedBlobs(t *testing.T) {
	root := t.TempDir()
	shared := fixtureLayer{MediaModel, []byte("GGUF-shared")}
	addModel(t, root, "a:1", "qwen2", shared, fixtureLayer{MediaSystem, []byte("only-a")})
	addModel(t, root, "b:1", "qwen2", shared)
	s := &Store{Root: root}

	a, err := s.Resolve("a:1")
	require.NoError(t, err)
	sysPath, _ := s.BlobPath(a.Manifest.Layers[1].Digest)

	require.NoError(t, s.Delete("a:1"))
	_, err = s.Resolve("a:1")
	assert.ErrorIs(t, err, ErrNotFound)
	assert.FileExists(t, a.ModelPath, "blob used by b:1 stays")
	assert.NoFileExists(t, sysPath)
	assert.ErrorIs(t, s.Delete("a:1"), ErrNotFound)
}
