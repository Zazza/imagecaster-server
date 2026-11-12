package config

import (
	"bytes"
	"errors"
	"log"
	"os"
	"runtime"

	"gopkg.in/yaml.v3"
)

const EngineMigrationBackupSuffix = ".bak-before-sdcpp"

var sdcppOnThisPlatform = func() bool { return SDCppSupported(runtime.GOOS, runtime.GOARCH) }

func migrateEngines(path string, data []byte) ([]byte, string) {
	if fileHasKey(data, "active_llm") {
		return data, backupActiveSD(path)
	}
	if !sdcppOnThisPlatform() {
		return data, ""
	}
	if _, err := os.Stat(path + EngineMigrationBackupSuffix); err == nil {
		log.Printf("[config] %s exists, config without active_llm is kept as is (rollback)", path+EngineMigrationBackupSuffix)
		return data, ""
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return data, ""
	}
	root := doc.Content[0]
	prev := mapValue(root, "active_sd")
	setMapValue(root, "active_sd", SDCppBackendKey)
	setMapValue(root, "active_llm", LLMLlamaCpp)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(4)
	if err := enc.Encode(&doc); err != nil {
		return data, ""
	}
	out := buf.Bytes()
	if err := writeMigrated(path, data, out); err != nil {
		log.Printf("[config] engines switched to sd.cpp and llama.cpp for this run only, config not saved: %v", err)
		return out, prev
	}
	log.Printf("[config] engines switched to sd.cpp and llama.cpp; models stay in their folders; old config saved as %s", path+EngineMigrationBackupSuffix)
	return out, prev
}

func backupActiveSD(path string) string {
	data, err := os.ReadFile(path + EngineMigrationBackupSuffix)
	if err != nil {
		return ""
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return ""
	}
	return mapValue(doc.Content[0], "active_sd")
}

func mapValue(m *yaml.Node, key string) string {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1].Value
		}
	}
	return ""
}

func writeMigrated(path string, old, migrated []byte) error {
	backup := path + EngineMigrationBackupSuffix
	created := false
	f, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	switch {
	case err == nil:
		created = true
		_, werr := f.Write(old)
		cerr := f.Close()
		if werr == nil {
			werr = cerr
		}
		if werr != nil {
			os.Remove(backup)
			return werr
		}
	case !errors.Is(err, os.ErrExist):
		return err
	}
	if err := replaceFile(path, migrated); err != nil {
		if created {
			os.Remove(backup)
		}
		return err
	}
	return nil
}

func replaceFile(path string, data []byte) error {
	perm := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		perm = st.Mode().Perm()
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func setMapValue(m *yaml.Node, key, value string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			v := m.Content[i+1]
			v.Kind, v.Tag, v.Value, v.Style, v.Content = yaml.ScalarNode, "!!str", value, 0, nil
			return
		}
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}
