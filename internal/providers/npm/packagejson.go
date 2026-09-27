package npm

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/V-Isa/awareof/internal/scope"
)

type packageConfig struct {
	Name               string
	Private            bool
	PublicationScripts []string
	Workspaces         json.RawMessage
	Digest             [sha256.Size]byte
}

type packageDocument struct {
	Name       json.RawMessage            `json:"name"`
	Private    json.RawMessage            `json:"private"`
	Scripts    map[string]json.RawMessage `json:"scripts"`
	Workspaces json.RawMessage            `json:"workspaces"`
}

type packageStatus uint8

const (
	packageReady packageStatus = iota
	packageMissing
	packageUnreadable
	packageInvalid
)

func (s packageStatus) summary(packageRoot scope.Path) string {
	name := "package.json"
	if packageRoot != "." {
		name = path.Join(string(packageRoot), "package.json")
	}
	switch s {
	case packageUnreadable:
		return name + " cannot be read safely"
	case packageInvalid:
		return name + " is not a supported valid package manifest"
	default:
		return name + " is unavailable"
	}
}

func readPackage(root *os.Root, packageRoot scope.Path) (packageConfig, packageStatus) {
	name := "package.json"
	if packageRoot != "." {
		name = path.Join(string(packageRoot), "package.json")
	}
	info, err := root.Lstat(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return packageConfig{}, packageMissing
	case err != nil, info.IsDir(), !info.Mode().IsRegular(), info.Size() > maxPackageJSONSize:
		return packageConfig{}, packageUnreadable
	}
	file, err := root.Open(name)
	if err != nil {
		return packageConfig{}, packageUnreadable
	}
	content, readErr := io.ReadAll(io.LimitReader(file, maxPackageJSONSize+1))
	after, statErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil || statErr != nil || closeErr != nil || len(content) > maxPackageJSONSize || !utf8.Valid(content) || !after.Mode().IsRegular() || !os.SameFile(info, after) {
		return packageConfig{}, packageUnreadable
	}
	config, err := parsePackage(content)
	if err != nil {
		return packageConfig{}, packageInvalid
	}
	config.Digest = sha256.Sum256(content)
	return config, packageReady
}

func parsePackage(content []byte) (packageConfig, error) {
	var document packageDocument
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	if err := decoder.Decode(&document); err != nil {
		return packageConfig{}, err
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return packageConfig{}, err
	}
	var config packageConfig
	if len(document.Name) != 0 {
		if err := json.Unmarshal(document.Name, &config.Name); err != nil {
			return packageConfig{}, fmt.Errorf("parse name: %w", err)
		}
	}
	if len(document.Private) != 0 {
		if err := json.Unmarshal(document.Private, &config.Private); err != nil {
			return packageConfig{}, fmt.Errorf("parse private: %w", err)
		}
	}
	for _, script := range publicationScripts {
		raw, ok := document.Scripts[script]
		if !ok {
			continue
		}
		var command string
		if err := json.Unmarshal(raw, &command); err != nil {
			return packageConfig{}, fmt.Errorf("parse script %s: %w", script, err)
		}
		if strings.TrimSpace(command) != "" {
			config.PublicationScripts = append(config.PublicationScripts, script)
		}
	}
	config.Workspaces = append(json.RawMessage(nil), document.Workspaces...)
	return config, nil
}
