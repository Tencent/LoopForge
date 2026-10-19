package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"agentflow/internal/managedcli"
)

// runCLI runs before opening storage or starting any platform services.
// Updater subprocess environment isolation is owned by managedcli.
func runCLI(ctx context.Context, action, provider string, timeout time.Duration, stdout io.Writer) error {
	root := strings.TrimSpace(os.Getenv("AGENTFLOW_MANAGED_CLI_ROOT"))
	if root == "" || timeout <= 0 {
		return fmt.Errorf("AGENTFLOW_MANAGED_CLI_ROOT and a positive -cli-timeout are required")
	}
	if action != "status" && action != "update" && action != "rollback" {
		return fmt.Errorf("invalid -cli-action: choose status, update, or rollback")
	}
	if action != "rollback" && provider != "" {
		return fmt.Errorf("-cli-provider is only supported with rollback")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if action == "status" {
		state, err := managedcli.ReadState(root)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(state)
	}
	manager, err := managedcli.NewManager(root)
	if err != nil {
		return err
	}
	var report managedcli.Report
	if action == "rollback" {
		report, err = manager.Rollback(ctx, provider)
	} else {
		report, err = manager.Update(ctx)
	}
	encodeErr := json.NewEncoder(stdout).Encode(report)
	if err != nil {
		return err
	}
	return encodeErr
}
