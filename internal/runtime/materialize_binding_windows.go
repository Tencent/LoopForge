package runtime

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func stageManagedBinding(_ string, binding managedBinding) error {
	return validateManagedBindingTarget(binding.target)
}

func finishManagedBindings(workspace string, bindings []managedBinding) error {
	// Bindings are registered parent-first. Copy their populated children
	// before taking the agent directory's snapshot of the completed view.
	copied := make(map[managedBinding]struct{}, len(bindings))
	for i := len(bindings) - 1; i >= 0; i-- {
		binding := bindings[i]
		if _, ok := copied[binding]; ok {
			continue
		}
		if err := materializeManagedBinding(workspace, binding.path, binding.target); err != nil {
			return err
		}
		copied[binding] = struct{}{}
	}
	return nil
}

func validateManagedBindingTarget(target string) error {
	if target == "" || filepath.IsAbs(target) || filepath.VolumeName(target) != "" || strings.HasPrefix(target, `\`) || strings.HasPrefix(target, "/") {
		return fmt.Errorf("AgentFlow binding target must be relative: %s", target)
	}
	return nil
}

// materializeManagedBinding creates an ordinary snapshot without symlink
// privileges. Every operation is confined to the existing control root.
func materializeManagedBinding(workspace, path, target string) error {
	if err := validateManagedBindingTarget(target); err != nil {
		return err
	}
	base, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}
	destination, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	control := filepath.Join(base, ".agentflow")
	source := filepath.Join(filepath.Dir(destination), target)
	sourceRel, err := managedBindingControlPath(control, source)
	if err != nil {
		return err
	}
	destinationRel, err := managedBindingControlPath(control, destination)
	if err != nil {
		return err
	}
	if rel, err := filepath.Rel(source, destination); err == nil && (rel == "." || filepath.IsLocal(rel)) {
		return fmt.Errorf("AgentFlow binding destination overlaps its source: %s", path)
	}
	workspaceRoot, err := os.OpenRoot(base)
	if err != nil {
		return err
	}
	defer workspaceRoot.Close()
	if err := rejectManagedBindingReparsePoints(workspaceRoot, ".agentflow"); err != nil {
		return err
	}
	root, err := workspaceRoot.OpenRoot(".agentflow")
	if err != nil {
		return err
	}
	defer root.Close()
	return copyManagedBinding(root, sourceRel, destinationRel)
}

func managedBindingControlPath(control, path string) (string, error) {
	rel, err := filepath.Rel(control, path)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("AgentFlow binding path is outside its control root: %s", path)
	}
	return rel, nil
}

func rejectManagedBindingReparsePoints(root *os.Root, rel string) error {
	if err := rejectWorkspaceSymlinks(root, rel); err != nil {
		return err
	}
	current := ""
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := root.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := rejectManagedBindingReparseInfo(current, info); err != nil {
			return err
		}
	}
	return nil
}

func rejectManagedBindingReparseInfo(path string, info os.FileInfo) error {
	attributes, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return fmt.Errorf("AgentFlow binding file attributes unavailable: %s", path)
	}
	if info.Mode()&os.ModeSymlink != 0 || attributes.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("AgentFlow binding path is a symlink or reparse point: %s", path)
	}
	return nil
}

func copyManagedBinding(root *os.Root, source, destination string) error {
	if err := rejectManagedBindingReparsePoints(root, source); err != nil {
		return err
	}
	if err := rejectManagedBindingReparsePoints(root, destination); err != nil {
		return err
	}
	if _, err := root.Lstat(destination); err == nil {
		return fmt.Errorf("AgentFlow binding destination already exists: %s", destination)
	} else if !os.IsNotExist(err) {
		return err
	}
	input, err := root.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	if err := rejectManagedBindingReparseInfo(source, info); err != nil {
		return err
	}
	if info.IsDir() {
		entries, err := input.ReadDir(-1)
		if err != nil {
			return err
		}
		if err := root.Mkdir(destination, info.Mode().Perm()); err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyManagedBinding(root, filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("AgentFlow binding source is not a regular file: %s", source)
	}
	output, err := root.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	return errors.Join(copyErr, output.Close())
}
