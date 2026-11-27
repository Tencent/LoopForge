package httpapi

import (
	"context"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestAssistantFailureReportsDiskFull(t *testing.T) {
	for _, err := range []error{
		&os.PathError{Op: "write", Path: "/private/workspace", Err: syscall.ENOSPC},
		errors.New("exit status 1; stderr=write /private/workspace: no space left on device"),
	} {
		failure := classifyAssistantFailure(context.Background(), err)
		if failure.Code != "DISK_FULL" || !strings.Contains(failure.Message, "磁盘") || strings.Contains(failure.Message, "/private/workspace") {
			t.Fatalf("disk-full failure not reported safely: %+v", failure)
		}
	}
}
