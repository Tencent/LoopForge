package notification

import (
	"strings"
	"testing"
)

// 850002 and 850003 are the same layer - the Bot's per-capability
// authorization - in its two states: never granted, and expired. Neither is
// fixed by retrying or by re-entering credentials, so both must read as an
// authorization the creator has to grant and must put the configuration in
// awaiting_authorization rather than a generic error.
func TestWeComAuthorizationFailuresAreActionable(t *testing.T) {
	for code, want := range map[int]string{
		850002: "授权管理",
		850003: "重新授权",
		851009: "审批",
		853004: "重新验证",
	} {
		err := failure(code)
		if !strings.Contains(err.Message, want) {
			t.Errorf("failure(%d) message = %q, want it to mention %q", code, err.Message, want)
		}
		if !err.awaitingAuthorization() {
			t.Errorf("failure(%d) is not reported as awaiting authorization", code)
		}
		if !strings.Contains(err.Message, "权限恢复后") || !strings.Contains(err.Message, "发送记录」点击「重试") {
			t.Errorf("failure(%d) does not explain how to retry after authorization: %q", code, err.Message)
		}
	}
	// 853006 is a tenant-level capability block: no authorization can fix it,
	// so it must not be shown as waiting for one.
	blocked := failure(853006)
	if blocked.awaitingAuthorization() {
		t.Error("853006 is reported as awaiting authorization, but no authorization resolves it")
	}
	if !strings.Contains(blocked.Message, "未开通") {
		t.Errorf("failure(853006) message = %q", blocked.Message)
	}
	// An unknown code keeps the generic advice and is not an authorization.
	other := failure(1)
	if other.awaitingAuthorization() || other.Message == "" {
		t.Errorf("failure(1) = %+v", other)
	}
}
