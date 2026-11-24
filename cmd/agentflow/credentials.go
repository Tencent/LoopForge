package main

import (
	"agentflow/internal/repo"
	"errors"
	"fmt"
	"io"
)

// runCredentialAction implements -credential-action. status counts plaintext
// and sealed Bot credentials per key id; reencrypt seals plaintext and
// rewraps old key ids with the current key, one row at a time. Only counts
// and key ids are printed, never secret values. reencrypt is re-entrant.
func runCredentialAction(s *repo.Store, action string, out io.Writer) error {
	var report repo.CredentialReport
	var err error
	switch action {
	case "status":
		report, err = s.BotCredentialStatus()
	case "reencrypt":
		report, err = s.ReencryptBotCredentials()
	default:
		return errors.New("-credential-action must be status or reencrypt")
	}
	if err != nil && !errors.Is(err, repo.ErrCredentialKeyMissing) {
		return err
	}
	fmt.Fprintf(out, "bot credentials %s: %s\n", action, report)
	for _, failure := range report.Failed {
		fmt.Fprintf(out, "failed %s\n", failure)
	}
	if err != nil {
		return fmt.Errorf("AGENTFLOW_CREDENTIAL_KEYS is required for reencrypt: %w", err)
	}
	if len(report.Failed) > 0 {
		return fmt.Errorf("%d credential(s) failed", len(report.Failed))
	}
	return nil
}
