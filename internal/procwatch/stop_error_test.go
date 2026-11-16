package procwatch

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStopErrorClassifiesCause(t *testing.T) {
	base := errors.New("signal: killed")
	opts := Options{Label: "克隆仓库 web", Stall: 90 * time.Second, StallEnv: "STALL_ENV", Timeout: 10 * time.Minute}
	cases := []struct {
		name    string
		cause   error
		stopped bool
		want    string
	}{
		{"stalled", errStalled, true, "克隆仓库 web 已 1m30s 没有任何输出，判定卡住并停止（可用 STALL_ENV 调整）: signal: killed"},
		{"timeout", errTimeout, true, "克隆仓库 web 超过 10m0s 仍未完成，已停止: signal: killed"},
		{"caller deadline", context.DeadlineExceeded, true, "克隆仓库 web 在调用方时限内没有完成（已运行 3s），已停止: signal: killed"},
		{"plain exit", nil, false, "signal: killed"},
		{"caller canceled", context.Canceled, false, "signal: killed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stopError(tc.cause, opts, 3*time.Second, base)
			if Stopped(got) != tc.stopped {
				t.Fatalf("Stopped = %v, want %v (%v)", Stopped(got), tc.stopped, got)
			}
			if !errors.Is(got, base) {
				t.Fatalf("stopError lost the original error: %v", got)
			}
			if got.Error() != tc.want {
				t.Fatalf("error = %q, want %q", got.Error(), tc.want)
			}
		})
	}
}

func TestFailureMessageAppendsLastLines(t *testing.T) {
	err := errors.New("exit status 128")
	if got, want := failureMessage("构建镜像", 2*time.Second, err, ""), "构建镜像 失败（耗时 2s）：exit status 128"; got != want {
		t.Fatalf("no tail = %q, want %q", got, want)
	}
	got := failureMessage("构建镜像", 2*time.Second, err, "a\nb\nc\nd")
	if want := "构建镜像 失败（耗时 2s）：exit status 128\nb\nc\nd"; got != want {
		t.Fatalf("with tail = %q, want %q", got, want)
	}
}
