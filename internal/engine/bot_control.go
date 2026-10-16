package engine

import (
	"agentflow/internal/domain"
	"fmt"
)

// NodeExpectation binds chat-card actions to the exact attempt displayed. The
// existing engine state lock and persistence conflict checks still apply.
type NodeExpectation struct {
	NodeID, Status       string
	Attempt, ReworkCount int
}

func checkNodeExpectation(run domain.Run, expected []NodeExpectation) error {
	if len(expected) == 0 {
		return nil
	}
	want := expected[0]
	node := run.Nodes[want.NodeID]
	if node == nil || node.Status != want.Status || node.Attempt != want.Attempt || node.ReworkCount != want.ReworkCount {
		return fmt.Errorf("通知对应的节点状态已变化，请查看最新消息")
	}
	return nil
}
