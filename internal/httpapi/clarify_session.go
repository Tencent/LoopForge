package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func clarifySessionBase() string {
	if override := strings.TrimSpace(os.Getenv("AGENTFLOW_CLARIFY_SESSION_DIR")); override != "" {
		return override
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		home = os.TempDir()
	}
	return filepath.Join(home, ".agentflow", "clarify-sessions")
}

func clarifyCLIHome(userID int64) string {
	return filepath.Join(clarifySessionBase(), strconv.FormatInt(userID, 10), "home")
}

// Older assessments have no owner metadata. Only resume those when the
// conversation actually exists inside the current responsible user's HOME.
func canResumeClarifySession(homeDir, sessionID string, sessionUserID, userID int64) bool {
	if sessionUserID != 0 && sessionUserID != userID {
		return false
	}
	if sessionID == "" || strings.ContainsAny(sessionID, `/\\*?[]`) || sessionID == "." || sessionID == ".." {
		return false
	}
	files, err := filepath.Glob(filepath.Join(homeDir, ".codebuddy", "projects", "*", sessionID+".jsonl"))
	if err != nil {
		return false
	}
	for _, file := range files {
		if info, err := os.Stat(file); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			return true
		}
	}
	return false
}

// CodeBuddy can report a missing resume conversation but still exit with 0.
// Inspect error envelopes as well as the OS exit status so the caller can
// rebuild the conversation from stored history instead of retrying resume.
func clarifySessionOutputError(output string) error {
	for _, line := range strings.Split(output, "\n") {
		var event struct {
			Type    string `json:"type"`
			IsError bool   `json:"is_error"`
		}
		if json.Unmarshal([]byte(line), &event) != nil || (event.Type != "error" && !(event.Type == "result" && event.IsError)) {
			continue
		}
		lower := strings.ToLower(line)
		if strings.Contains(lower, "sessionnotfounderror") || strings.Contains(lower, "no conversation found with session id") {
			return fmt.Errorf("澄清会话不存在，无法续接")
		}
	}
	return nil
}

func clarifyWorkspaceDir(userID int64, key string) string {
	key = filepath.Base(strings.TrimSpace(key))
	if key == "" || key == "." || key == ".." {
		key = "draft"
	}
	return filepath.Join(clarifySessionBase(), strconv.FormatInt(userID, 10), "ws", key)
}

func ensureClarifySessionDirs(userID int64) error {
	return os.MkdirAll(filepath.Join(clarifyCLIHome(userID), "tmp"), 0o700)
}

func ensureClarifyWorkspace(userID int64, key string) (string, error) {
	dir := clarifyWorkspaceDir(userID, key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func resetClarifyWorkspaceLinks(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			_ = os.Remove(path)
		}
	}
}

// materializeClarifyRepo places a real directory tree at dest so CodeBuddy
// Read/Grep/Glob can see the files. Out-of-tree symlinks are invisible to those
// tools even when they point at a valid cache checkout.
//
// First turns (refresh=true) recopy from src. Later turns reuse an existing
// real directory. Leftover symlinks from older sessions are always replaced.
func materializeClarifyRepo(src, dest string, refresh bool) error {
	src = filepath.Clean(strings.TrimSpace(src))
	dest = filepath.Clean(strings.TrimSpace(dest))
	if src == "" || dest == "" {
		return fmt.Errorf("empty clarify repo path")
	}
	if src == dest {
		return fmt.Errorf("clarify repo source and destination are the same")
	}
	info, err := os.Lstat(dest)
	if err == nil {
		isLink := info.Mode()&os.ModeSymlink != 0
		if !isLink && info.IsDir() && !refresh {
			return nil
		}
		if err := os.RemoveAll(dest); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return cloneClarifyTree(src, dest)
}

func cloneClarifyTree(src, dest string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !srcInfo.IsDir() {
		return fmt.Errorf("clarify repo source is not a directory: %s", src)
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() && d.Name() == ".git" {
			return fs.SkipDir
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := dest
		if rel != "." {
			target = filepath.Join(dest, rel)
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := os.Link(path, target); err == nil {
			return nil
		}
		return copyClarifyFile(path, target)
	})
}

func copyClarifyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	mode := info.Mode().Perm()
	if mode == 0 {
		mode = 0o600
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

type clarifyCommandOpts struct {
	Bin, Model, Prompt, RunID, SessionID string
	Resume                               bool
	Native                               bool
	MCPConfig, Settings                  string
	SubmitOnly                           bool
}

func prepareClarifyCommand(bin, model, prompt, runID, sessionID string, resume bool) (runtime.CommandSpec, error) {
	return prepareClarifyCommandWith(clarifyCommandOpts{Bin: bin, Model: model, Prompt: prompt, RunID: runID, SessionID: sessionID, Resume: resume, Native: true})
}

func prepareClarifyCommandWith(opts clarifyCommandOpts) (runtime.CommandSpec, error) {
	args := []string{"-p", "--max-turns", "500", "--output-format", "stream-json", "--include-partial-messages"}
	if strings.TrimSpace(opts.MCPConfig) != "" {
		args = append(args, "--mcp-config", opts.MCPConfig, "--strict-mcp-config")
		if strings.TrimSpace(opts.Settings) != "" {
			args = append(args, "--settings", opts.Settings)
		}
	}
	inv := runtime.InvocationOptions{RunID: opts.RunID, NodeID: "clarify"}
	if opts.Resume && strings.TrimSpace(opts.SessionID) != "" {
		inv.ResumeSessionID = opts.SessionID
	} else if strings.TrimSpace(opts.SessionID) != "" {
		args = append(args, "--session-id", opts.SessionID)
	}
	ex := domain.ExecutorDefinition{Kind: "codebuddy", Binary: opts.Bin, Model: opts.Model, Args: args}
	command, err := runtime.PrepareProviderCommand(ex, opts.Prompt, "local", inv)
	if err != nil {
		return runtime.CommandSpec{}, err
	}
	command.Argv = applyNativeToolSafety(command.Argv, AgentToolClarify)
	if opts.SubmitOnly {
		command.Argv = appendDisallowedTools(command.Argv, "Read,Grep,Glob")
	}
	if strings.TrimSpace(opts.MCPConfig) != "" {
		command = command.WithMCPStartupWait(agentToolSessionMCPStartupWait)
	}
	return command, nil
}

func appendDisallowedTools(argv []string, extra string) []string {
	extra = strings.TrimSpace(extra)
	if extra == "" {
		return argv
	}
	for i, arg := range argv {
		if arg == "--disallowedTools" && i+1 < len(argv) {
			cur := strings.TrimSpace(argv[i+1])
			if cur == "" {
				argv[i+1] = extra
			} else {
				argv[i+1] = cur + "," + extra
			}
			return argv
		}
	}
	return append(argv, "--disallowedTools", extra)
}

func captureClarifySessionID(line string) string {
	var event map[string]any
	if json.Unmarshal([]byte(line), &event) != nil {
		return ""
	}
	sid, _ := event["session_id"].(string)
	return strings.TrimSpace(sid)
}

// clarifyResumeTurnPrompt re-anchors every resumed round on the original
// work item: long sessions otherwise drift toward whatever the latest answer
// or the code just read happened to mention.
func clarifyResumeTurnPrompt(title, message string, assistantRounds int) string {
	msg := strings.TrimSpace(message)
	if msg == "" {
		msg = "请基于会话里已有信息继续澄清。"
	}
	var b strings.Builder
	b.WriteString("这是用户对本轮澄清的回复。结合会话中已读过的代码、原单、评论和对话继续，不要重复已问过的问题，不要再读已经成功读过的同一原单/评论/文档。")
	if title = strings.TrimSpace(title); title != "" {
		b.WriteString("\n范围锚点：本次只澄清「" + title + "」本身，遵守首轮约定的范围规则；用户回答若带出这条需求以外的新诉求，不要沿着追问，写进 summary 的「不在本次范围」并建议另建需求。")
	}
	b.WriteString(clarifyRoundBudgetHint(assistantRounds))
	b.WriteString("完成后调用 submit_clarification。\n用户：")
	b.WriteString(msg)
	return b.String()
}

// clarifyRoundBudgetHint nudges the model to conclude once several rounds
// have passed; without a stop condition it keeps finding "new" questions,
// and the only new ones left are about neighbouring features.
func clarifyRoundBudgetHint(assistantRounds int) string {
	if assistantRounds < 3 {
		return ""
	}
	return fmt.Sprintf("\n已经澄清了 %d 轮：除非还有不回答就无法开工的缺口，本轮直接给出结论（ready=true），剩余不确定点按现有代码惯例做合理假设并写进 summary。", assistantRounds)
}

func clarifyNativeRetryPrompt() string {
	return "刚才未成功提交有效的澄清结果，本轮尚未结束。请根据工具校验错误修正并重新调用 submit_clarification。ready=false 必须提供具体 questions，不能只有 summary；选择题 options 至少包含两个不同选项，开放填写题设 freeText=true、options=[]。如果其实已经没有阻塞开工的缺口，就提交 ready=true 并给出 summary。"
}

func isClarifyProtocolObject(v any) bool {
	m, ok := v.(map[string]any)
	if !ok || m == nil {
		return false
	}
	if _, ok := m["summary"]; ok {
		return true
	}
	if _, ok := m["questions"]; ok {
		return true
	}
	if _, ok := m["ready"]; ok {
		return true
	}
	if _, ok := m["mcpCalls"]; ok {
		return true
	}
	return false
}

func classifyClarifyProtocolError(err error) string {
	if err == nil {
		return "ok"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "未返回 JSON"):
		return "invalid_json"
	case strings.Contains(msg, "缺少 summary"):
		return "ready_missing_summary"
	case strings.Contains(msg, "既没有 questions"):
		return "empty_questions"
	case strings.Contains(msg, "问题文本为空"):
		return "empty_question_text"
	default:
		return "invalid_schema"
	}
}

func clarifySystemPrompt(intro, mcpStep, focus string) string {
	return fmt.Sprintf(`%s
1. 用 Read/Grep/Glob 读取当前工作目录代码，定位这条工作项要改的位置和现有实现
%s
3. 围绕%s，基于代码、原单/评论和对话历史判断还缺什么阻塞开工的信息
4. 完成后调用 submit_clarification。只有调用该工具才算结束本轮。

范围规则（最重要）：
- 「## 需求」里的标题和描述就是本次范围的边界。只澄清把这一条需求做完所必需的信息，不负责规划整个模块或项目
- 读代码是为了定位改动点、确认现有行为，不是为了寻找还能顺带做的事。代码里看到的相邻功能、历史问题、可优化点、未来扩展，都不要提问
- 不要提出会扩大范围的问题或选项（例如「是否同时支持 X」「要不要顺便改 Y」「其他页面要不要一起改」）。确实值得提醒的相邻事项，只在 summary 的「不在本次范围」里列一句
- 用户回答里带出这条需求以外的新诉求时，不要沿着追问，记入「不在本次范围」并建议另建需求
- 不要为了凑问题而提问：能按现有代码惯例合理假设的细节直接假设，写进 summary；没有阻塞开工的缺口就设 ready=true

问题要具体、基于代码：
- 不问"范围是什么"，而是问"需求说改 X，代码里 X 由 A 处理，是在 A 上改还是新增入口？"——问题必须落在需求原文已经提到的东西上
- 每轮只问 1-3 个最关键的新问题，放入 questions，不要只在 summary 中列出缺口
- ready=false 时必须有问题。选择题 options 至少提供两个不同、具体可操作的选项，选项都应在本需求范围内；需要用户填写路径、数量等内容时使用 freeText=true、options=[]、multiSelect=false，不编造选项
- ready=true 时 summary 按「本次范围 / 不在本次范围 / 验收标准 / 假设」分段给出结论
- 工具校验失败时修正参数并重新提交
- TAPD 的 id、workspace_id、entry_id 必须用字符串
- 思考用中文对用户说正在读代码或原单
`, intro, mcpStep, focus)
}
