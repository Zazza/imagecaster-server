package llamacpp

import (
	"fmt"
	"log"
	"sort"
)

type Library struct {
	Own     *Store
	Foreign []*Store
}

func (l *Library) stores() []*Store {
	return append([]*Store{l.Own}, l.Foreign...)
}

func (l *Library) List() ([]Model, error) {
	own, err := l.Own.List()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]Model, 0, len(own))
	for _, m := range own {
		seen[m.Name.String()] = true
		out = append(out, m)
	}
	for _, s := range l.Foreign {
		list, err := s.List()
		if err != nil {
			log.Printf("[llamacpp] read Ollama models in %s: %v", s.Root, err)
			continue
		}
		for _, m := range list {
			if seen[m.Name.String()] {
				continue
			}
			seen[m.Name.String()] = true
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name.String() < out[j].Name.String() })
	return out, nil
}

func (l *Library) Resolve(name string) (Model, error) {
	n, err := ParseName(name)
	if err != nil {
		return Model{}, err
	}
	for _, s := range l.stores() {
		if m, ok := s.exact(n); ok {
			return m, nil
		}
	}
	list, err := l.List()
	if err == nil {
		if m, ok := findFold(list, n); ok {
			return m, nil
		}
	}
	return Model{}, fmt.Errorf("%w: %s", ErrNotFound, n)
}

func readOnlyError(m Model) error {
	root := ""
	if m.store != nil {
		root = m.store.Root
	}
	return fmt.Errorf("%s: %w in %s; remove it there with `ollama rm %s`", m.Name, ErrReadOnly, root, m.Name)
}

func (l *Library) Delete(name string) error {
	m, err := l.Resolve(name)
	if err != nil {
		return err
	}
	if m.ReadOnly {
		return readOnlyError(m)
	}
	return l.Own.Delete(m.Name.String())
}

func (l *Library) Text(m Model, mediaType string) string {
	return l.Own.Text(m, mediaType)
}

func (l *Library) Params(m Model) map[string]any {
	return l.Own.Params(m)
}

func (l *Library) WritePreset(path string, opt PresetOptions) (map[string]string, error) {
	list, err := l.List()
	if err != nil {
		return nil, err
	}
	return writePreset(list, path, opt)
}
