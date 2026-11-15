package dockerproxy

import (
	"strings"
	"unicode"
)

// The daemon decodes request bodies into Go structs, and encoding/json
// matches field names case-insensitively (with Unicode folding: "K" U+212A
// matches "k"). The proxy checks the body as a map, where lookups are exact.
// Without this step {"hostconfig":{"privileged":true}} would pass every check
// and still reach the daemon as HostConfig.Privileged. canonicalizeKeys
// renames every key that folds to a field the proxy reads to that field's
// exact name, and refuses an object with two keys that fold to the same name
// (which one the daemon picks is not something to rely on).

// checkedFields are the Docker API field names the proxy reads or writes.
// TestCheckedFieldsCoverProxyLookups keeps it complete.
var checkedFields = indexFields(
	// containers/create, commit, exec, update
	"HostConfig", "NetworkingConfig", "EndpointsConfig", "Labels", "Image",
	"Privileged", "CapAdd", "CgroupParent", "MaskedPaths", "ReadonlyPaths",
	"VolumeDriver", "Runtime", "Devices", "DeviceCgroupRules", "DeviceRequests",
	"SecurityOpt", "PidMode", "IpcMode", "UTSMode", "UsernsMode", "CgroupnsMode",
	"NetworkMode", "VolumesFrom", "Mounts", "Binds", "Links",
	// mounts
	"Type", "Source", "Target", "ReadOnly", "BindOptions", "Propagation",
	"VolumeOptions", "DriverConfig", "NoCopy", "Subpath",
	// networks and volumes
	"Name", "Driver", "DriverOpts", "Options", "IPAM", "Config", "ConfigFrom",
	"ConfigOnly", "Container",
)

// freeFormFields hold maps keyed by user data (label names, paths, network
// names, ports); their keys are never renamed. Their values are still
// objects with API fields where they are objects.
var freeFormFields = map[string]bool{
	"Labels": true, "EndpointsConfig": true, "DriverOpts": true, "Options": true,
	"ExposedPorts": true, "Volumes": true, "Sysctls": true, "StorageOpt": true,
	"Tmpfs": true, "PortBindings": true, "Annotations": true, "AuxiliaryAddresses": true,
}

func indexFields(names ...string) map[string]string {
	index := make(map[string]string, len(names))
	for _, name := range names {
		index[foldKey(name)] = name
	}
	return index
}

// foldKey maps a key to a form shared by every key bytes.EqualFold (and so
// encoding/json) treats as the same: each rune becomes the smallest member
// of its case-folding orbit, which also catches "\u212a" for "k".
func foldKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		least := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			if f < least {
				least = f
			}
		}
		b.WriteRune(least)
	}
	return b.String()
}

func canonicalizeKeys(v any) error {
	return canonicalize(v, false)
}

func canonicalize(v any, freeForm bool) error {
	switch t := v.(type) {
	case map[string]any:
		seen := make(map[string]string, len(t))
		for key := range t {
			folded := foldKey(key)
			// Free-form maps (labels, options) are decoded with exact keys.
			if other, dup := seen[folded]; dup && !freeForm {
				return forbidden("请求体里 " + other + " 与 " + key + " 只差大小写，无法判断守护进程会用哪个")
			}
			seen[folded] = key
		}
		keys := make([]string, 0, len(t))
		for key := range t {
			keys = append(keys, key)
		}
		for _, key := range keys {
			value := t[key]
			name := key
			if !freeForm {
				if canonical, ok := checkedFields[foldKey(key)]; ok && canonical != key {
					delete(t, key)
					t[canonical] = value
					name = canonical
				}
			}
			if err := canonicalize(value, !freeForm && freeFormFields[name]); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range t {
			if err := canonicalize(item, false); err != nil {
				return err
			}
		}
	}
	return nil
}
