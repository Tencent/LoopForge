package workspacessh

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

const clientCommand = "desktop-ssh"
const aliasPrefix = "loopforge-"

type clientSettings struct {
	Server   string
	Token    string
	Identity string
}

// RunCLI is shared by the two binaries already distributed to Docker and
// local execution machines. The sandbox command only reads and writes stdio.
func RunCLI(args []string) (bool, error) {
	if len(args) == 0 || (args[0] != ServerCommand && args[0] != clientCommand) {
		return false, nil
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if args[0] == ServerCommand {
		return true, ServeSandbox(ctx, os.Stdin, os.Stdout, "/workspace")
	}
	return true, runClient(ctx, args[1:], os.Stdin, os.Stdout)
}

func runClient(ctx context.Context, args []string, input io.ReadCloser, output io.Writer) error {
	if len(args) > 0 && args[0] == "configure" {
		return configureClient(args[1:], input, output)
	}
	if len(args) != 2 || (args[0] != "proxy" && args[0] != "host-key") {
		return fmt.Errorf("usage: agentflow-runner desktop-ssh configure -server URL -identity PRIVATE_KEY (personal API token on stdin)")
	}
	settings, runID, err := settingsForHost(args[1])
	if err != nil {
		return err
	}
	base := settings.Server + "/api/runs/" + url.PathEscape(runID) + "/workspace/ssh"
	headers := http.Header{"Authorization": {"Bearer " + settings.Token}}
	if args[0] == "host-key" {
		requestCtx, cancel := context.WithTimeout(ctx, HandshakeTimeout)
		defer cancel()
		request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, base+"/host-key", nil)
		if err != nil {
			return err
		}
		request.Header = headers
		client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := client.Do(request)
		if err != nil {
			return err
		}
		if response.StatusCode != http.StatusOK {
			return clientResponseError(response)
		}
		defer response.Body.Close()
		var payload struct{ HostKey string }
		if err := json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&payload); err != nil {
			return err
		}
		if _, err := ClientPublicKey([]byte(payload.HostKey)); err != nil {
			return err
		}
		_, err = fmt.Fprintf(output, "%s %s\n", args[1], strings.TrimSpace(payload.HostKey))
		return err
	}

	key, err := os.ReadFile(settings.Identity + ".pub")
	if err != nil {
		return err
	}
	if _, err := ClientPublicKey(key); err != nil {
		return err
	}
	headers.Set(PublicKeyHeader, base64.RawStdEncoding.EncodeToString(key))
	address, err := WebSocketURL(base, "")
	if err != nil {
		return err
	}
	dialer := websocket.Dialer{Proxy: http.ProxyFromEnvironment, HandshakeTimeout: HandshakeTimeout}
	for attempt := 0; ; attempt++ {
		socket, response, err := dialer.DialContext(ctx, address, headers)
		if err == nil {
			Relay(ctx, NewSocket(socket), &clientStdio{input, output})
			return nil
		}
		if response == nil {
			return err
		}
		apiErr := clientResponseError(response)
		// VS Code can start multiple SSH connections during launch. A
		// refused preparation lease is safe to retry before SSH begins.
		if apiErr.code != "workspace_busy" || attempt == 4 {
			return apiErr
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

type workspaceSSHAPIError struct {
	status  int
	code    string
	message string
}

func (e *workspaceSSHAPIError) Error() string {
	if e.message != "" {
		return fmt.Sprintf("workspace SSH: %s (HTTP %d)", e.message, e.status)
	}
	return fmt.Sprintf("workspace SSH: platform returned HTTP %d", e.status)
}

func clientResponseError(response *http.Response) *workspaceSSHAPIError {
	defer response.Body.Close()
	var payload struct{ Error, Code string }
	_ = json.NewDecoder(io.LimitReader(response.Body, 2048)).Decode(&payload)
	return &workspaceSSHAPIError{status: response.StatusCode, code: payload.Code, message: payload.Error}
}

func configureClient(args []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("desktop-ssh configure", flag.ContinueOnError)
	server := flags.String("server", "", "LoopForge origin, e.g. https://loopforge.example")
	identity := flags.String("identity", "", "OpenSSH private key path with an adjacent .pub file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *identity == "" {
		return fmt.Errorf("provide -server and -identity")
	}
	origin, err := platformOrigin(*server)
	if err != nil {
		return err
	}
	keyPath, err := filepath.Abs(*identity)
	if err != nil {
		return err
	}
	if _, err := os.Stat(keyPath); err != nil {
		return err
	}
	key, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		return err
	}
	if _, err := ClientPublicKey(key); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if strings.ContainsAny(executable+keyPath, "\r\n%") {
		return fmt.Errorf("SSH helper and identity paths must not contain line breaks or percent signs")
	}
	tokenBytes, err := io.ReadAll(io.LimitReader(input, 4097))
	if err != nil {
		return err
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" || len(token) > 4096 || strings.ContainsAny(token, " \t\r\n") {
		return fmt.Errorf("provide a personal API token on stdin")
	}
	platform := hex.EncodeToString([]byte(origin))
	path, err := settingsPath(platform)
	if err != nil {
		return err
	}
	if err := saveSettings(path, clientSettings{Server: origin, Token: token, Identity: keyPath}); err != nil {
		return err
	}
	shellExecutable := "'" + strings.ReplaceAll(executable, "'", "'\"'\"'") + "'"
	_, err = fmt.Fprintf(output, "Host %s%s-*\n    User loopforge\n    IdentitiesOnly yes\n    IdentityFile %q\n    ProxyCommand %s %s proxy '%%n'\n    KnownHostsCommand %q %s host-key %%n\n    StrictHostKeyChecking yes\n    UserKnownHostsFile /dev/null\n",
		aliasPrefix, platform, keyPath, shellExecutable, clientCommand, executable, clientCommand)
	return err
}

func platformOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("provide a LoopForge platform origin without a path")
	}
	host := strings.ToLower(u.Hostname())
	ip := net.ParseIP(host)
	if u.Scheme != "https" && !(u.Scheme == "http" && (host == "localhost" || ip != nil && ip.IsLoopback())) {
		return "", fmt.Errorf("LoopForge desktop SSH requires HTTPS (HTTP is allowed for local testing)")
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := u.Port(); port != "" && !(u.Scheme == "https" && port == "443") && !(u.Scheme == "http" && port == "80") {
		host += ":" + port
	}
	return u.Scheme + "://" + host, nil
}

func settingsForHost(alias string) (clientSettings, string, error) {
	suffix, ok := strings.CutPrefix(alias, aliasPrefix)
	platform, run, found := strings.Cut(suffix, "-")
	encodedOrigin, originErr := hex.DecodeString(platform)
	runID, runErr := hex.DecodeString(run)
	if !ok || !found || originErr != nil || runErr != nil || len(encodedOrigin) == 0 || !validRunID(string(runID)) {
		return clientSettings{}, "", fmt.Errorf("invalid LoopForge SSH host alias")
	}
	path, err := settingsPath(platform)
	if err != nil {
		return clientSettings{}, "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return clientSettings{}, "", fmt.Errorf("configure desktop SSH for %s: %w", encodedOrigin, err)
	}
	var settings clientSettings
	if err := json.Unmarshal(raw, &settings); err != nil {
		return clientSettings{}, "", err
	}
	origin, err := platformOrigin(settings.Server)
	if err != nil || origin != string(encodedOrigin) || settings.Token == "" {
		return clientSettings{}, "", fmt.Errorf("desktop SSH configuration does not match the platform origin")
	}
	return settings, string(runID), nil
}

func validRunID(id string) bool {
	if len(id) == 0 || len(id) > 128 || id == "." || id == ".." {
		return false
	}
	for _, ch := range id {
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_' || ch == '.' {
			continue
		}
		return false
	}
	return true
}

func settingsPath(platform string) (string, error) {
	if alternative := os.Getenv("LOOPFORGE_DESKTOP_SSH_CONFIG_DIR"); alternative != "" {
		return filepath.Join(alternative, platform+".json"), nil
	}
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "LoopForge", "desktop-ssh", platform+".json"), nil
}

func saveSettings(path string, settings clientSettings) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = json.NewEncoder(file).Encode(settings)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}

type clientStdio struct {
	io.ReadCloser
	io.Writer
}
