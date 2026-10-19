// Package managedcli manages all supported platform-owned CLI installations.
// Personal runners and container images continue to manage their own binaries.
package managedcli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var releaseVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// Installation points at one immutable, version-specific npm installation.
// Authentication/configuration homes are deliberately not part of this record.
type Installation struct {
	Version     string    `json:"version"`
	Binary      string    `json:"binary"`
	Integrity   string    `json:"integrity,omitempty"`
	InstalledAt time.Time `json:"installedAt,omitempty"`
}

type Inventory map[string]Installation

func (i Inventory) BinaryFor(provider string) (string, bool) {
	v, ok := i[provider]
	return v.Binary, ok && v.Binary != ""
}

type State struct {
	LastCheck  time.Time         `json:"lastCheck"`
	Candidates Inventory         `json:"candidates"`
	Current    Inventory         `json:"current"`
	Previous   Inventory         `json:"previous"`
	Rejected   map[string]string `json:"rejected,omitempty"`
	UpdatedAt  time.Time         `json:"updatedAt,omitempty"`
}

// State and its rollback pointers are committed in one atomic rename.
func ReadState(root string) (State, error) {
	if err := validRoot(root); err != nil {
		return State{}, err
	}
	state := &State{}
	if err := readJSON(filepath.Join(root, "state.json"), &state); err != nil && !os.IsNotExist(err) {
		return State{}, err
	}
	if state == nil {
		return State{}, errors.New("managed CLI state must be a JSON object")
	}
	for _, inventory := range []*Inventory{&state.Current, &state.Previous, &state.Candidates} {
		if *inventory == nil {
			*inventory = Inventory{}
		}
	}
	if state.Rejected == nil {
		state.Rejected = map[string]string{}
	}
	if err := validateInventory(root, state.Current); err != nil {
		return State{}, fmt.Errorf("current inventory: %w", err)
	}
	return *state, nil
}

func ReadInventory(root string) (Inventory, error) {
	state, err := ReadState(root)
	return state.Current, err
}

// PinInventory fixes the complete inventory for a run, including an empty one
// before initial provisioning. Pins live outside agent-controlled workspaces.
// Publishing via link makes concurrent first claims observe a complete file.
// Old versions and pins are not automatically deleted: suspended/resumable runs
// may still reference them after the runner or updater restarts.
func PinInventory(root, runKey string) (Inventory, error) {
	if strings.TrimSpace(runKey) == "" {
		return nil, errors.New("managed CLI run key is required")
	}
	if err := prepareDirectory(root, "pins"); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(runKey))
	path := filepath.Join(root, "pins", hex.EncodeToString(sum[:])+".json")
	pinned := Inventory{}
	if err := readJSON(path, &pinned); err == nil {
		return pinned, validateInventory(root, pinned)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	current, err := ReadInventory(root)
	if err != nil {
		return nil, err
	}
	tmp, err := writeTemporaryJSON(filepath.Dir(path), current)
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp) // exactly the temporary file created above
	if err := os.Link(tmp, path); err != nil && !os.IsExist(err) {
		return nil, fmt.Errorf("publish run inventory: %w", err)
	}
	if err := readJSON(path, &pinned); err != nil {
		return nil, err
	}
	return pinned, validateInventory(root, pinned)
}

func validRoot(root string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator) {
		return errors.New("managed CLI root must be a clean absolute non-root directory")
	}
	if info, err := os.Lstat(root); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("managed CLI root must be a real directory, not a symlink")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func prepareDirectory(root, rel string) error {
	if err := validRoot(root); err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	if rel == "" {
		return nil
	}
	if filepath.IsAbs(rel) || filepath.Clean(rel) != rel || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("invalid managed CLI subdirectory")
	}
	path := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		path = filepath.Join(path, part)
		if err := os.Mkdir(path, 0o700); err != nil && !os.IsExist(err) {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("managed CLI directory is not a real directory: %s", path)
		}
	}
	return nil
}

func validateInventory(root string, inventory Inventory) error {
	if inventory == nil {
		return errors.New("managed CLI inventory must be a JSON object")
	}
	for provider, installed := range inventory {
		if _, ok := providerSpecs[provider]; !ok {
			return fmt.Errorf("unknown managed CLI provider %q", provider)
		}
		if !releaseVersion.MatchString(installed.Version) {
			return fmt.Errorf("invalid release version for %s", provider)
		}
		base := filepath.Join(root, "providers", provider)
		if !within(base, installed.Binary) {
			return fmt.Errorf("%s executable escapes its managed installation", provider)
		}
		resolvedBase, err := filepath.EvalSymlinks(base)
		if err != nil {
			return err
		}
		resolved, err := filepath.EvalSymlinks(installed.Binary)
		if err != nil {
			return err
		}
		resolvedRoot, err := filepath.EvalSymlinks(root)
		if err != nil || !within(filepath.Join(resolvedRoot, "providers", provider), resolvedBase) && resolvedBase != filepath.Join(resolvedRoot, "providers", provider) {
			return errors.New("managed provider directory escapes root")
		}
		if !within(resolvedBase, resolved) {
			return errors.New("managed executable symlink escapes installation")
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
			return fmt.Errorf("managed CLI %s is not executable", provider)
		}
	}
	return nil
}

func within(root, path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func readJSON(path string, v any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return fmt.Errorf("invalid managed CLI JSON file: %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, (1<<20)+1))
	if err := decoder.Decode(v); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("unexpected trailing JSON content")
	}
	return nil
}

func writeTemporaryJSON(dir string, v any) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, ".managed-cli-*")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if _, err = f.Write(append(data, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

func atomicJSON(path string, v any) error {
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("refuse to replace non-regular managed state file")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	tmp, err := writeTemporaryJSON(filepath.Dir(path), v)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	return os.Rename(tmp, path)
}
