//go:build !windows

package runtime

func stageManagedBinding(_ string, binding managedBinding) error {
	return replaceRelativeSymlink(binding.path, binding.target)
}

func finishManagedBindings(_ string, _ []managedBinding) error {
	return nil
}

func materializeManagedBinding(_ string, path, target string) error {
	return replaceRelativeSymlink(path, target)
}
