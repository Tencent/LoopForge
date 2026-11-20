package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// DiscoverRunnerLoginModels uses the same login source as a conversation,
// with an empty working directory and a temporary private HOME.
func DiscoverRunnerLoginModels(ctx context.Context, provider, binary string) ([]domain.CLIModel, error) {
	login, ok := runnerLoginSpec(provider)
	if !ok {
		return nil, fmt.Errorf("该 CLI 暂不支持获取账号模型")
	}
	root, err := os.MkdirTemp("", "loopforge-login-models-")
	if err != nil {
		return nil, fmt.Errorf("无法创建模型查询目录")
	}
	defer os.RemoveAll(root)
	cleanup, loginEnv, err := prepareConversationRunnerLogin(root, login, true)
	if err != nil {
		return nil, err
	}
	if cleanup != nil {
		defer func() {
			if err := cleanup(); err != nil {
				log.Printf("runner login models: cleanup %s: %v", login.label, err)
			}
		}()
	}
	home := filepath.Join(root, "home")
	env := map[string]string{"NO_COLOR": "1", "CI": "1"}
	if login.configEnv != "" {
		env[login.configEnv] = filepath.Join(home, login.dir)
	}
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS"} {
		if value, ok := os.LookupEnv(key); ok {
			env[key] = value
		}
	}
	path := filepath.Dir(binary) + string(os.PathListSeparator) + os.Getenv("PATH")
	env["PATH"] = path
	args := []string{"app-server"}
	if login.binding == BindClaudeCode {
		args = []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--setting-sources", ""}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	environment := appendEnvironment(terminalBaseEnvironment(home), env, loginEnv)
	if provider == AgentCLIAntigravity {
		cmd := exec.CommandContext(ctx, binary, "models")
		cmd.Dir, cmd.Env, cmd.WaitDelay = root, environment, time.Second
		output, err := cmd.Output()
		if err != nil || len(output) > 2<<20 {
			return nil, fmt.Errorf("Antigravity 无法返回账号模型，请检查执行机器上的 CLI 版本和登录状态")
		}
		models := antigravityLoginModels(string(output))
		if len(models) == 0 {
			return nil, fmt.Errorf("Antigravity 未返回可用的账号模型")
		}
		return models, nil
	}
	if login.binding == BindClaudeCode {
		status := exec.CommandContext(ctx, binary, "auth", "status")
		status.Dir, status.Env, status.WaitDelay = root, environment, time.Second
		output, err := status.Output()
		var auth struct {
			LoggedIn bool `json:"loggedIn"`
		}
		if err != nil || json.Unmarshal(output, &auth) != nil || !auth.LoggedIn {
			return nil, fmt.Errorf("执行机器上的 Claude Code 登录态不可用，请检查 CLI 版本和账号登录")
		}
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir, cmd.Env = root, environment
	cmd.WaitDelay = time.Second
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("无法连接 %s 查询模型", login.label)
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("无法读取 %s 的模型列表", login.label)
	}
	defer output.Close()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("无法启动 %s 查询模型", login.label)
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	scanner := bufio.NewScanner(io.LimitReader(output, 2<<20))
	scanner.Buffer(make([]byte, 16<<10), 1<<20)
	send := json.NewEncoder(input)
	var models []domain.CLIModel
	if login.binding == BindCodex {
		models = codexLoginModels(send, scanner)
	} else {
		models = claudeLoginModels(send, scanner)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("查询 %s 模型超时", login.label)
	}
	if models == nil {
		return nil, fmt.Errorf("%s 无法返回账号模型，请检查执行机器上的 CLI 版本和登录状态", login.label)
	}
	return models, nil
}

func antigravityLoginModels(output string) []domain.CLIModel {
	var models []domain.CLIModel
	seen := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		id, name, found := strings.Cut(strings.TrimSpace(line), "\t")
		if !found || id == "" || strings.ContainsAny(id, " \r\n") || seen[id] {
			continue
		}
		seen[id] = true
		models = append(models, domain.CLIModel{ID: id, Name: strings.TrimSpace(name)})
	}
	return models
}

func codexLoginModels(send *json.Encoder, scanner *bufio.Scanner) []domain.CLIModel {
	if send.Encode(map[string]any{
		"id": 1, "method": "initialize",
		"params": map[string]any{"clientInfo": map[string]string{"name": "loopforge", "version": "1.0.0"}},
	}) != nil || codexLoginResult(scanner, 1) == nil {
		return nil
	}
	if send.Encode(map[string]string{"method": "initialized"}) != nil ||
		send.Encode(map[string]any{"id": 2, "method": "account/read", "params": map[string]bool{"refreshToken": false}}) != nil {
		return nil
	}
	var account struct {
		Account *struct{} `json:"account"`
	}
	if json.Unmarshal(codexLoginResult(scanner, 2), &account) != nil || account.Account == nil {
		return nil
	}
	if send.Encode(map[string]any{"id": 3, "method": "model/list", "params": map[string]any{"limit": 100, "includeHidden": false}}) != nil {
		return nil
	}
	var result struct {
		Data []struct {
			Model       string `json:"model"`
			DisplayName string `json:"displayName"`
			Efforts     *[]struct {
				Value string `json:"reasoningEffort"`
			} `json:"supportedReasoningEfforts"`
		} `json:"data"`
	}
	if json.Unmarshal(codexLoginResult(scanner, 3), &result) != nil || result.Data == nil {
		return nil
	}
	models := make([]domain.CLIModel, 0, len(result.Data))
	for _, item := range result.Data {
		if id := strings.TrimSpace(item.Model); id != "" {
			model := domain.CLIModel{ID: id, Name: item.DisplayName}
			if item.Efforts != nil {
				levels := make([]string, 0, len(*item.Efforts))
				for _, level := range *item.Efforts {
					levels = append(levels, level.Value)
				}
				model.DiscoveredReasoningEfforts = &levels
			}
			models = append(models, model)
		}
	}
	return models
}

func codexLoginResult(scanner *bufio.Scanner, id int) json.RawMessage {
	for scanner.Scan() {
		var message struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(scanner.Bytes(), &message) == nil && message.ID == id {
			return message.Result
		}
	}
	return nil
}

func claudeLoginModels(send *json.Encoder, scanner *bufio.Scanner) []domain.CLIModel {
	const id = "loopforge-models"
	if send.Encode(map[string]any{
		"type": "control_request", "request_id": id,
		"request": map[string]string{"subtype": "initialize"},
	}) != nil {
		return nil
	}
	for scanner.Scan() {
		var message struct {
			Type     string `json:"type"`
			Response struct {
				RequestID string `json:"request_id"`
				Subtype   string `json:"subtype"`
				Response  struct {
					Models []struct {
						Value          string    `json:"value"`
						DisplayName    string    `json:"displayName"`
						SupportsEffort *bool     `json:"supportsEffort"`
						EffortLevels   *[]string `json:"supportedEffortLevels"`
					} `json:"models"`
				} `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal(scanner.Bytes(), &message) != nil || message.Type != "control_response" || message.Response.RequestID != id {
			continue
		}
		if message.Response.Subtype != "success" || message.Response.Response.Models == nil {
			return nil
		}
		models := make([]domain.CLIModel, 0, len(message.Response.Response.Models))
		for _, item := range message.Response.Response.Models {
			if id := strings.TrimSpace(item.Value); id != "" {
				levels := item.EffortLevels
				if item.SupportsEffort != nil && !*item.SupportsEffort {
					levels = &[]string{}
				}
				models = append(models, domain.CLIModel{ID: id, Name: item.DisplayName, DiscoveredReasoningEfforts: levels})
			}
		}
		return models
	}
	return nil
}
