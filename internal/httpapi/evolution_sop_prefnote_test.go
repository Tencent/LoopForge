package httpapi

import "testing"

func TestHasSOPPreferenceNoteAcceptsBothWordings(t *testing.T) {
	for _, summary := range []string{
		"【LOOP 优化优先级：Token】近 3 次运行",
		"【SOP 优化优先级：Token】近 3 次运行",
		"LOOP 优化偏好已更新",
		"SOP 优化偏好已更新",
	} {
		if !hasSOPPreferenceNote(summary) {
			t.Errorf("hasSOPPreferenceNote(%q) = false, want true", summary)
		}
	}
	for _, summary := range []string{"", "节点失败 2 次"} {
		if hasSOPPreferenceNote(summary) {
			t.Errorf("hasSOPPreferenceNote(%q) = true, want false", summary)
		}
	}
}
