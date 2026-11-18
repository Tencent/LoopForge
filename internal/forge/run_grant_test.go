package forge

import (
	"strings"
	"testing"
	"time"
)

func TestForgeRunGrantSignatureAndExpiry(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "forgejo-node-signing-test")
	now := time.Now()
	grant := RunGrant{RunID: "run-a", NodeID: "mr", Generation: 3, Attempt: 2}
	token, err := SignRunGrant(grant, now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyRunGrant(token, now)
	if err != nil || verified.RunID != grant.RunID || verified.NodeID != grant.NodeID || verified.Generation != 3 || verified.Attempt != 2 {
		t.Fatalf("verified grant = %+v, %v", verified, err)
	}
	for _, candidate := range []string{"", strings.Repeat("x", 4097), token + "x", "lfforge1.invalid.signature"} {
		if _, err := VerifyRunGrant(candidate, now); err == nil {
			t.Fatal("an invalid node grant was accepted")
		}
	}
	if _, err := VerifyRunGrant(token, now.Add(48*time.Hour)); err == nil {
		t.Fatal("an expired node grant was accepted")
	}
	t.Setenv("AGENTFLOW_SESSION_SECRET", "rotated-forgejo-node-signing-test")
	if _, err := VerifyRunGrant(token, now); err == nil {
		t.Fatal("a grant signed before key rotation was accepted")
	}
}
