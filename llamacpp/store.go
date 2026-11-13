package llamacpp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	DefaultHost      = "registry.ollama.ai"
	defaultNamespace = "library"
	defaultTag       = "latest"

	MediaModel     = "application/vnd.ollama.image.model"
	MediaProjector = "application/vnd.ollama.image.projector"
	MediaTemplate  = "application/vnd.ollama.image.template"
	MediaSystem    = "application/vnd.ollama.image.system"
	MediaParams    = "application/vnd.ollama.image.params"
	MediaManifest  = "application/vnd.docker.distribution.manifest.v2+json"
)

var (
	ErrNotFound    = errors.New("model not found")
	ErrBadName     = errors.New("invalid model name")
	ErrUnsupported = errors.New("model is in Ollama's own format, llama.cpp cannot open it")
	ErrReadOnly    = errors.New("model belongs to a separate Ollama install")
)

var unsupportedFamilies = map[string]bool{"qwen25vl": true}

var embedFamilies = map[string]bool{"bert": true, "nomic-bert": true}

var (
	partRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	tagRe  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
)

type Name struct {
	Host      string
	Namespace string
	Model     string
	Tag       string
}

func ParseName(s string) (Name, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Name{}, ErrBadName
	}
	n := Name{Host: DefaultHost, Namespace: defaultNamespace, Tag: defaultTag}
	if i := strings.LastIndex(s, ":"); i > strings.LastIndex(s, "/") {
		n.Tag = s[i+1:]
		s = s[:i]
	}
	parts := strings.Split(s, "/")
	switch len(parts) {
	case 1:
		n.Model = parts[0]
	case 2:
		n.Namespace, n.Model = parts[0], parts[1]
	case 3:
		n.Host, n.Namespace, n.Model = parts[0], parts[1], parts[2]
	default:
		return Name{}, fmt.Errorf("%w: %q", ErrBadName, s)
	}
	for _, p := range []string{n.Host, n.Namespace, n.Model} {
		if !partRe.MatchString(p) || strings.Contains(p, "..") {
			return Name{}, fmt.Errorf("%w: %q", ErrBadName, s)
		}
	}
	if !tagRe.MatchString(n.Tag) {
		return Name{}, fmt.Errorf("%w: tag %q", ErrBadName, n.Tag)
	}
	return n, nil
}

func (n Name) String() string {
	switch {
	case n.Host == DefaultHost && n.Namespace == defaultNamespace:
		return n.Model + ":" + n.Tag
	case n.Host == DefaultHost:
		return n.Namespace + "/" + n.Model + ":" + n.Tag
	}
	return n.Host + "/" + n.Namespace + "/" + n.Model + ":" + n.Tag
}

func (n Name) manifestRel() string {
	return filepath.Join(n.Host, n.Namespace, n.Model, n.Tag)
}

type Layer struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

type Manifest struct {
	SchemaVersion int     `json:"schemaVersion"`
	MediaType     string  `json:"mediaType"`
	Config        Layer   `json:"config"`
	Layers        []Layer `json:"layers"`
}

type ModelConfig struct {
	ModelFormat   string   `json:"model_format"`
	ModelFamily   string   `json:"model_family"`
	ModelFamilies []string `json:"model_families"`
	ModelType     string   `json:"model_type"`
	FileType      string   `json:"file_type"`
}

type Model struct {
	Name      Name
	Manifest  Manifest
	Digest    string
	Modified  time.Time
	Config    ModelConfig
	ModelPath string
	Projector string
	Size      int64
	ReadOnly  bool
	store     *Store
}

func (m Model) Family() string {
	return m.Config.ModelFamily
}

func (m Model) Vision() bool {
	return m.Projector != ""
}

func (m Model) Embedding() bool {
	return embedFamilies[m.Config.ModelFamily]
}

func (m Model) Supported() error {
	if unsupportedFamilies[m.Config.ModelFamily] {
		return fmt.Errorf("%s: %w (family %s); use a GGUF model from hf.co with a separate projector", m.Name, ErrUnsupported, m.Config.ModelFamily)
	}
	if m.ModelPath == "" {
		return fmt.Errorf("%s: no model layer in manifest", m.Name)
	}
	return nil
}

type Store struct {
	Root     string
	ReadOnly bool
}

func (s *Store) manifestsDir() string { return filepath.Join(s.Root, "manifests") }
func (s *Store) blobsDir() string     { return filepath.Join(s.Root, "blobs") }

func digestHex(d string) (string, bool) {
	h, ok := strings.CutPrefix(d, "sha256:")
	if !ok || len(h) != 64 {
		return "", false
	}
	if _, err := hex.DecodeString(h); err != nil {
		return "", false
	}
	return strings.ToLower(h), true
}

func (s *Store) BlobPath(digest string) (string, error) {
	h, ok := digestHex(digest)
	if !ok {
		return "", fmt.Errorf("bad digest %q", digest)
	}
	return filepath.Join(s.blobsDir(), "sha256-"+h), nil
}

func (s *Store) readBlob(digest string, limit int64) ([]byte, error) {
	p, err := s.BlobPath(digest)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if st.Size() > limit {
		return nil, fmt.Errorf("blob %s too large", digest)
	}
	return os.ReadFile(p)
}

func (s *Store) load(n Name, raw []byte, mod time.Time) (Model, error) {
	var mf Manifest
	if err := json.Unmarshal(raw, &mf); err != nil {
		return Model{}, fmt.Errorf("manifest %s: %w", n, err)
	}
	sum := sha256.Sum256(raw)
	m := Model{Name: n, Manifest: mf, Digest: hex.EncodeToString(sum[:]), Modified: mod, Size: mf.Config.Size, ReadOnly: s.ReadOnly, store: s}
	if b, err := s.readBlob(mf.Config.Digest, 1<<20); err == nil {
		_ = json.Unmarshal(b, &m.Config)
	}
	for _, l := range mf.Layers {
		m.Size += l.Size
		p, err := s.BlobPath(l.Digest)
		if err != nil {
			continue
		}
		switch l.MediaType {
		case MediaModel:
			if m.ModelPath == "" {
				m.ModelPath = p
			}
		case MediaProjector:
			if m.Projector == "" {
				m.Projector = p
			}
		}
	}
	return m, nil
}

func (s *Store) Resolve(name string) (Model, error) {
	n, err := ParseName(name)
	if err != nil {
		return Model{}, err
	}
	if m, ok := s.exact(n); ok {
		return m, nil
	}
	list, err := s.List()
	if err == nil {
		if m, ok := findFold(list, n); ok {
			return m, nil
		}
	}
	return Model{}, fmt.Errorf("%w: %s", ErrNotFound, n)
}

func (s *Store) exact(n Name) (Model, bool) {
	raw, st, err := readFileStat(filepath.Join(s.manifestsDir(), n.manifestRel()))
	if err != nil {
		return Model{}, false
	}
	m, err := s.load(n, raw, st.ModTime())
	return m, err == nil
}

func findFold(list []Model, n Name) (Model, bool) {
	want := strings.ToLower(n.String())
	for _, m := range list {
		if strings.ToLower(m.Name.String()) == want {
			return m, true
		}
	}
	return Model{}, false
}

func readFileStat(p string) ([]byte, fs.FileInfo, error) {
	st, err := os.Stat(p)
	if err != nil {
		return nil, nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > 1<<20 {
		return nil, nil, fmt.Errorf("not a manifest: %s", p)
	}
	raw, err := os.ReadFile(p)
	return raw, st, err
}

func (s *Store) List() ([]Model, error) {
	root := s.manifestsDir()
	var out []Model
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root && errors.Is(err, fs.ErrNotExist) {
				return filepath.SkipAll
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) != 4 {
			return nil
		}
		n := Name{Host: parts[0], Namespace: parts[1], Model: parts[2], Tag: parts[3]}
		raw, st, err := readFileStat(p)
		if err != nil {
			return nil
		}
		m, err := s.load(n, raw, st.ModTime())
		if err != nil {
			return nil
		}
		out = append(out, m)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name.String() < out[j].Name.String() })
	return out, nil
}

func (s *Store) Delete(name string) error {
	m, err := s.Resolve(name)
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(s.manifestsDir(), m.Name.manifestRel())); err != nil {
		return err
	}
	s.pruneDirs(filepath.Dir(filepath.Join(s.manifestsDir(), m.Name.manifestRel())))
	used := map[string]bool{}
	others, err := s.List()
	if err != nil {
		return err
	}
	for _, o := range others {
		used[o.Manifest.Config.Digest] = true
		for _, l := range o.Manifest.Layers {
			used[l.Digest] = true
		}
	}
	for _, l := range append([]Layer{m.Manifest.Config}, m.Manifest.Layers...) {
		if used[l.Digest] {
			continue
		}
		p, err := s.BlobPath(l.Digest)
		if err != nil {
			continue
		}
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (s *Store) pruneDirs(dir string) {
	root := s.manifestsDir()
	for dir != root && strings.HasPrefix(dir, root) {
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

func (s *Store) Text(m Model, mediaType string) string {
	src := s
	if m.store != nil {
		src = m.store
	}
	for _, l := range m.Manifest.Layers {
		if l.MediaType == mediaType {
			b, err := src.readBlob(l.Digest, 1<<20)
			if err == nil {
				return string(b)
			}
		}
	}
	return ""
}

func (s *Store) Params(m Model) map[string]any {
	raw := s.Text(m, MediaParams)
	if raw == "" {
		return nil
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil
	}
	return p
}

var idRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func presetID(n Name) string {
	return strings.Trim(idRe.ReplaceAllString(n.String(), "_"), "_")
}

type PresetOptions struct {
	ContextSize int
}

func (s *Store) WritePreset(path string, opt PresetOptions) (map[string]string, error) {
	list, err := s.List()
	if err != nil {
		return nil, err
	}
	return writePreset(list, path, opt)
}

func writePreset(list []Model, path string, opt PresetOptions) (map[string]string, error) {
	ids := map[string]string{}
	seen := map[string]bool{}
	var b strings.Builder
	b.WriteString("version = 1\n\n[*]\n")
	if opt.ContextSize > 0 {
		fmt.Fprintf(&b, "c = %d\n", opt.ContextSize)
	}
	b.WriteString("np = 1\n")
	for _, m := range list {
		if m.Supported() != nil {
			continue
		}
		id := presetID(m.Name)
		for i := 2; seen[id]; i++ {
			id = fmt.Sprintf("%s_%d", presetID(m.Name), i)
		}
		seen[id] = true
		ids[m.Name.String()] = id
		fmt.Fprintf(&b, "\n[%s]\nmodel = %s\n", id, m.ModelPath)
		if m.Projector != "" {
			fmt.Fprintf(&b, "mmproj = %s\n", m.Projector)
		}
		if m.Embedding() {
			b.WriteString("embeddings = true\n")
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}
	return ids, nil
}
