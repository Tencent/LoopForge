package domain

import (
	"strings"
	"testing"
)

func TestGongfengClosed(t *testing.T) {
	closed := Issue{Source: "gongfeng", ExternalRef: &ExternalRef{ExternalState: "closed"}}
	if !closed.GongfengClosed() {
		t.Fatal("closed Gongfeng issue must be GongfengClosed")
	}
	opened := Issue{Source: "gongfeng", ExternalRef: &ExternalRef{ExternalState: "opened"}}
	if opened.GongfengClosed() {
		t.Fatal("opened Gongfeng issue must still be triggerable")
	}
	localDone := Issue{Source: "gongfeng", Status: "done"}
	if localDone.GongfengClosed() {
		t.Fatal("local status alone must not block trigger")
	}
	tapd := Issue{Source: "tapd", ExternalRef: &ExternalRef{ExternalState: "closed"}}
	if tapd.GongfengClosed() {
		t.Fatal("TAPD closed items are not GongfengClosed")
	}
}

func TestIssueCloseSource(t *testing.T) {
	manual := Issue{Status: "closed", CloseSource: "manual"}
	if !manual.ManuallyClosed() || manual.TrackerClosed() {
		t.Fatalf("manual close classification: %+v", manual)
	}
	legacy := Issue{Status: "closed"}
	if !legacy.ManuallyClosed() {
		t.Fatal("older manually closed issues must remain manual")
	}
	external := Issue{Source: "tapd", Status: "closed", CloseSource: "external", ExternalRef: &ExternalRef{ExternalState: "已关闭"}}
	if external.ManuallyClosed() || !external.TrackerClosed() {
		t.Fatalf("external close classification: %+v", external)
	}
	if !strings.Contains(external.ExecutionBlockedReason(), "外部系统") {
		t.Fatalf("external close block reason = %q", external.ExecutionBlockedReason())
	}
}

func TestMergedDeliveryBlocksUntilReopened(t *testing.T) {
	merged := Issue{Source: "tapd", DeliveryState: "merged"}
	if !merged.DeliveryLocked() || merged.ExecutionBlockedReason() == "" {
		t.Fatal("merged delivery must block execution")
	}
	if !strings.Contains(merged.ExecutionBlockedReason(), "交付闭环") {
		t.Fatalf("expected delivery-closed wording without merged MR, got %q", merged.ExecutionBlockedReason())
	}
	if merged.DeliveryLockedLabel() != "交付闭环" {
		t.Fatalf("expected delivery-closed label without merged MR, got %q", merged.DeliveryLockedLabel())
	}
	merged.DeliveryMRURL = "https://git.example.com/org/repo/merge_requests/1"
	merged.DeliveryMRState = "opened"
	if strings.Contains(merged.ExecutionBlockedReason(), "MR 已合入") {
		t.Fatalf("opened MR must not read as merged, got %q", merged.ExecutionBlockedReason())
	}
	merged.DeliveryMRState = "merged"
	if !strings.Contains(merged.ExecutionBlockedReason(), "MR 已合入") {
		t.Fatalf("expected MR merged wording, got %q", merged.ExecutionBlockedReason())
	}
	if merged.DeliveryLockedLabel() != "MR 已合入" {
		t.Fatalf("expected MR merged label, got %q", merged.DeliveryLockedLabel())
	}
	merged.DeliveryMRState = "integrated"
	if merged.DeliveryLockedLabel() != "已合入基线" {
		t.Fatalf("expected baseline label, got %q", merged.DeliveryLockedLabel())
	}
	if !strings.Contains(merged.ExecutionBlockedReason(), "已合入基线") {
		t.Fatalf("expected baseline block reason, got %q", merged.ExecutionBlockedReason())
	}
	merged.DeliveryMRState = "closed"
	if merged.DeliveryLockedLabel() != "MR 已关闭" {
		t.Fatalf("expected closed label, got %q", merged.DeliveryLockedLabel())
	}
	noLink := Issue{Status: "done", DeliveryState: "merged", DeliveryMRState: "closed"}
	if noLink.DeliveryLockedLabel() != "已关闭" || !strings.Contains(noLink.ExecutionBlockedReason(), "工作项已关闭") {
		t.Fatalf("closed MR state without a link is a manual close, label=%q reason=%q", noLink.DeliveryLockedLabel(), noLink.ExecutionBlockedReason())
	}
	if !strings.Contains(merged.ExecutionBlockedReason(), "MR 已关闭") {
		t.Fatalf("expected closed block reason, got %q", merged.ExecutionBlockedReason())
	}
	merged.Status = "closed"
	if merged.DeliveryLockedLabel() != "关闭" || !strings.Contains(merged.ExecutionBlockedReason(), "工作项已关闭") {
		t.Fatalf("persisted manual close must override MR state: label=%q reason=%q", merged.DeliveryLockedLabel(), merged.ExecutionBlockedReason())
	}
	merged.Status = "done"
	merged.DeliveryState = "reopened"
	if merged.DeliveryLocked() || merged.ExecutionBlockedReason() != "" {
		t.Fatal("explicitly reopened delivery must be triggerable")
	}
}
