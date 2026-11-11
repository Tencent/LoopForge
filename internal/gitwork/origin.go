package gitwork

import (
	"context"
	"fmt"
	"strings"
)

// RequireOriginURLs checks every effective fetch and push destination before a
// workspace is published. Git expands insteadOf/pushInsteadOf here, and --all
// includes additional push URLs rather than checking only the first endpoint.
// matches compares each URL to the caller's trusted repository identity.
// Errors deliberately omit URLs and Git output: workspace config is untrusted
// and may contain credentials belonging to someone other than this requester.
func RequireOriginURLs(ctx context.Context, workspace string, matches func(string) bool) error {
	if matches == nil {
		return fmt.Errorf("repository origin identity validator is missing")
	}
	for _, push := range []bool{false, true} {
		args := []string{"remote", "get-url", "--all"}
		kind := "fetch"
		if push {
			args = append(args, "--push")
			kind = "push"
		}
		args = append(args, "origin")
		output, err := C(ctx, workspace, args...)
		if err != nil || strings.TrimSpace(output) == "" {
			return fmt.Errorf("cannot verify repository origin %s destination", kind)
		}
		for _, remote := range strings.Split(output, "\n") {
			remote = strings.TrimSpace(remote)
			if remote == "" || !matches(remote) {
				return fmt.Errorf("repository origin %s destination does not match the configured repository", kind)
			}
		}
	}
	return nil
}
