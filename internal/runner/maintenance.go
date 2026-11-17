package runner

import (
	"context"
	"errors"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/managedcli"
)

const runnerMaintenanceKey = "runner"

type runnerMaintenanceState struct {
	mu      sync.Mutex
	active  string
	results map[string]domain.RunnerMaintenanceResult
	jobs    sync.WaitGroup
}

func (d *Daemon) cliUpdateRuntime(provider string) (domain.RunnerRuntime, bool) {
	for _, rt := range d.currentRuntimes() {
		if rt.Provider == provider && rt.SupportsCLIUpdate() {
			return rt, true
		}
	}
	return domain.RunnerRuntime{}, false
}

// The callback starts work after the acceptance reply has been sent. A
// browser disconnect does not interrupt a CLI update or the Runner restart.
func (d *Daemon) maintenanceResponse(ctx, daemonCtx context.Context, request *domain.RunnerMaintenanceRequest) (domain.RunnerWorkspaceResponse, func()) {
	if request == nil {
		return domain.RunnerWorkspaceResponse{Status: http.StatusBadRequest, Error: "缺少更新请求"}, nil
	}
	provider := request.Provider
	key, version := runnerMaintenanceKey, runnerVersion
	updateAvailable := true
	if provider != "" {
		var rt domain.RunnerRuntime
		rt, updateAvailable = d.cliUpdateRuntime(provider)
		key, version = provider, rt.Version
	}

	if !request.Apply {
		d.maintenance.mu.Lock()
		result, found := d.maintenance.results[key]
		d.maintenance.mu.Unlock()
		if found && (provider != "" || request.StatusOnly || result.Status == "updating" || result.Status == "restarting") {
			return runnerWorkspacePayload(result), nil
		}
		if provider != "" {
			if !updateAvailable {
				return runnerWorkspacePayload(cliUpdateManualResult()), nil
			}
			return runnerWorkspacePayload(domain.RunnerMaintenanceResult{Status: "ready", Current: version, CanUpdate: true}), nil
		}
		return runnerWorkspacePayload(d.inspectRunnerUpdate(ctx)), nil
	}
	if !updateAvailable {
		return domain.RunnerWorkspaceResponse{Status: http.StatusConflict, Error: cliUpdateManualResult().Message}, nil
	}

	if provider == "" && d.cfg.DisableAutoUpdate {
		return domain.RunnerWorkspaceResponse{Status: http.StatusConflict, Error: "此 Runner 已关闭平台更新，请在执行机器上更新"}, nil
	}
	d.updateMu.RLock()
	defer d.updateMu.RUnlock()
	d.maintenance.mu.Lock()
	defer d.maintenance.mu.Unlock()
	if d.updateReady.Load() {
		return domain.RunnerWorkspaceResponse{Status: http.StatusConflict, Error: "Runner 正在重启，请稍后重试"}, nil
	}
	if d.maintenance.active != "" {
		if d.maintenance.active == key {
			return runnerWorkspacePayload(d.maintenance.results[key]), nil
		}
		return domain.RunnerWorkspaceResponse{Status: http.StatusConflict, Error: "此机器正在更新，请稍后重试"}, nil
	}
	if d.maintenance.results == nil {
		d.maintenance.results = make(map[string]domain.RunnerMaintenanceResult)
	}
	pending := domain.RunnerMaintenanceResult{Status: "updating", Current: version}
	d.maintenance.results[key], d.maintenance.active = pending, key
	return runnerWorkspacePayload(pending), func() {
		d.maintenance.jobs.Add(1)
		go d.runMaintenance(daemonCtx, key, provider, version)
	}
}

func (d *Daemon) inspectRunnerUpdate(ctx context.Context) domain.RunnerMaintenanceResult {
	binary, err := currentRunnerExecutable()
	if err == nil {
		var local, expected string
		local, expected, err = d.runnerUpdateHashes(ctx, binary)
		if err == nil {
			result := domain.RunnerMaintenanceResult{Status: "current", Current: runnerVersion}
			if !strings.EqualFold(local, expected) {
				result.Status, result.CanUpdate = "available", !d.cfg.DisableAutoUpdate
				if !result.CanUpdate {
					result.Message = "此 Runner 已关闭平台更新，请在执行机器上更新"
				}
			}
			return result
		}
	}
	if ctx.Err() == nil {
		log.Printf("runner maintenance check: %v", err)
	}
	return domain.RunnerMaintenanceResult{Status: "failed", Current: runnerVersion, Message: "无法检查平台提供的 Runner 构建"}
}

func (d *Daemon) runMaintenance(ctx context.Context, key, provider, version string) {
	defer d.maintenance.jobs.Done()
	var result domain.RunnerMaintenanceResult
	if provider == "" {
		updateCtx, cancel := context.WithTimeout(ctx, 7*time.Minute)
		defer cancel()
		result = d.applyRunnerUpdate(updateCtx)
	} else {
		updateCtx, cancel := context.WithTimeout(ctx, 45*time.Minute)
		defer cancel()
		result = d.applyCLIUpdate(updateCtx, provider)
	}
	result.Current = version
	d.maintenance.mu.Lock()
	d.maintenance.results[key], d.maintenance.active = result, ""
	d.maintenance.mu.Unlock()
	if result.Status == "restarting" && d.cancelClaimLoop != nil {
		d.cancelClaimLoop()
	}
}

func (d *Daemon) applyCLIUpdate(ctx context.Context, provider string) domain.RunnerMaintenanceResult {
	rt, ok := d.cliUpdateRuntime(provider)
	if !ok {
		return cliUpdateManualResult()
	}
	if slices.Contains(rt.Capabilities, domain.RuntimeCapabilityNativeUpdate) {
		if result := d.applyNativeCLIUpdate(ctx, rt); result.Status != "manual" {
			return result
		}
	}
	if d.cfg.ManagedCLIRoot != "" && slices.Contains(rt.Capabilities, domain.RuntimeCapabilityManagedUpdate) {
		return d.applyManagedCLIUpdate(ctx, provider)
	}
	return cliUpdateManualResult()
}

func (d *Daemon) applyRunnerUpdate(ctx context.Context) domain.RunnerMaintenanceResult {
	binary, err := currentRunnerExecutable()
	if err == nil {
		var updated bool
		updated, err = d.installRunnerUpdate(ctx, binary)
		if err == nil {
			if updated {
				return domain.RunnerMaintenanceResult{Status: "restarting"}
			}
			return domain.RunnerMaintenanceResult{Status: "current"}
		}
	}
	if errors.Is(err, errRunnerUpdateBusy) {
		return domain.RunnerMaintenanceResult{Status: "failed", Message: "机器上有运行中的工作，请空闲后重试"}
	}
	log.Printf("manual runner update: %v", err)
	return domain.RunnerMaintenanceResult{Status: "failed", Message: "Runner 下载、校验或安装失败，请查看执行机器日志"}
}

func (d *Daemon) applyManagedCLIUpdate(ctx context.Context, provider string) domain.RunnerMaintenanceResult {
	manager, err := managedcli.NewManager(d.cfg.ManagedCLIRoot)
	if err == nil {
		var report managedcli.Report
		report, err = manager.UpdateProvider(ctx, provider)
		if err == nil && len(report.Results) == 1 {
			result := report.Results[0]
			switch result.Status {
			case "updated":
				d.refreshRuntimes(ctx)
				select {
				case d.heartbeatWake <- struct{}{}:
				default:
				}
				return domain.RunnerMaintenanceResult{Status: "updated", Version: result.To}
			case "unchanged":
				return domain.RunnerMaintenanceResult{Status: "current", Version: result.From}
			case "skipped":
				return domain.RunnerMaintenanceResult{Status: "skipped", Message: "当前没有可安装的托管版本"}
			}
		}
	}
	log.Printf("manual CLI update (%s): %v", provider, err)
	return domain.RunnerMaintenanceResult{Status: "failed", Message: "CLI 更新失败，请查看执行机器日志"}
}
