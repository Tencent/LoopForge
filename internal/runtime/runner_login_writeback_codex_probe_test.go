package runtime

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	goRuntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Explicit opt-in: run the official binary against FAKE credentials and a
// loopback issuer, with macOS additionally denying access to real credentials
// and all non-loopback networking. This does not validate a real provider's
// policy, model inference, or the proposed identity oracle.
func TestManagedLoginOfficialCodex(t *testing.T) {
	binary := os.Getenv("LOOPFORGE_PROBE_CODEX_BIN")
	if binary == "" {
		t.Skip("set LOOPFORGE_PROBE_CODEX_BIN to explicitly test an official binary with fake auth")
	}
	if goRuntime.GOOS != "darwin" || !filepath.IsAbs(binary) {
		t.Fatal("this opt-in probe requires macOS sandbox-exec and an absolute binary path")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	codexHome := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexHome, 0700); err != nil {
		t.Fatal(err)
	}
	jwt := func() string {
		claims, _ := json.Marshal(map[string]any{
			"sub": "fake-user-a", "email": "owner@example.test", "exp": time.Now().Add(time.Hour).Unix(),
			"https://api.openai.com/auth": map[string]any{
				"chatgpt_account_id": "fake-account-a", "chatgpt_user_id": "fake-user-a", "chatgpt_plan_type": "plus",
			},
		})
		return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(claims) + ".fake-signature"
	}
	var mu sync.Mutex
	calls, rejectedEgress := 0, 0
	latest := "fake-refresh-0"
	issuer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/token" && r.Method == http.MethodPost {
			var req struct {
				Refresh string `json:"refresh_token"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Refresh != latest {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
				return
			}
			calls++
			latest = fmt.Sprintf("fake-refresh-%d", calls)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": jwt(), "id_token": jwt(), "refresh_token": latest, "expires_in": 3600,
			})
			return
		}
		if r.Method == http.MethodConnect || r.URL.IsAbs() {
			rejectedEgress++
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/accounts/check") {
			_ = json.NewEncoder(w).Encode(map[string]any{"accounts": []any{map[string]any{
				"id": "fake-account-a", "plan_type": "plus", "workspace_backend_origin": "https://" + r.Host,
				"account_routing_override": "NO_CONSTRAINT", "name": "Fake isolated account",
			}}})
			return
		}
		_, _ = io.WriteString(w, `{"models":[]}`)
	}))
	// rustls correctly rejects a CA certificate used directly as a TLS leaf.
	// Use a distinct test CA and server leaf, rather than httptest's default.
	_, caKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "LoopForge fake issuer CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, caKey.Public(), caKey)
	if err != nil {
		t.Fatal(err)
	}
	_, leafKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames: []string{"localhost"}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, leafKey.Public(), caKey)
	if err != nil {
		t.Fatal(err)
	}
	issuer.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER, caDER}, PrivateKey: leafKey}}}
	issuer.StartTLS()
	defer issuer.Close()
	caPath := filepath.Join(root, "fake-issuer-ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0600); err != nil {
		t.Fatal(err)
	}
	config := "cli_auth_credentials_store = \"file\"\nchatgpt_base_url = " + fmt.Sprintf("%q\n", issuer.URL+"/chatgpt/")
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	initial := probeCredential("codex", "fake-account-a", probeTokens{Access: jwt(), Refresh: latest, ID: jwt()})
	host := t.TempDir()
	login, _ := runnerLoginSpec("codex")
	source := fileRunnerLoginSource(login, host)
	if err := os.WriteFile(source.path, initial, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_REFRESH_TOKEN_URL_OVERRIDE", issuer.URL+"/token")
	manager := &runnerLoginManager{stateDir: t.TempDir(), client: issuer.Client()}
	lease, err := newRunnerLoginLease(context.Background(), manager, source, false)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.close()
	staged, err := lease.credential()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), staged, 0600); err != nil {
		t.Fatal(err)
	}
	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	// sandbox-exec is scoped to this child. It changes no host permissions or
	// trust settings. Literal profile strings use JSON quoting, not a shell.
	profile := `(version 1) (allow default) (deny network*) (allow network-outbound (remote ip "localhost:*"))`
	for _, path := range []string{filepath.Join(realHome, ".codex"), filepath.Join(realHome, ".claude"), filepath.Join(realHome, ".gemini"), filepath.Join(realHome, "Library", "Keychains")} {
		profile += fmt.Sprintf(" (deny file-read* file-write* (subpath %q))", path)
	}
	profile += fmt.Sprintf(" (deny file-write* (subpath %q))", realHome)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", "-p", profile, binary, "app-server")
	cmd.Dir = root
	// A clean environment is essential: inherit neither the desktop app's auth
	// injection nor any API key, credential home, issuer override or proxy.
	cmd.Env = []string{
		"PATH=/usr/bin:/bin", "HOME=" + home, "CODEX_HOME=" + codexHome, "TMPDIR=" + root,
		"XDG_CONFIG_HOME=" + home, "XDG_DATA_HOME=" + home, "XDG_CACHE_HOME=" + home,
		"CODEX_REFRESH_TOKEN_URL_OVERRIDE=" + lease.endpoint,
		"CODEX_CA_CERTIFICATE=" + caPath, "SSL_CERT_FILE=" + caPath,
		"CODEX_APP_SERVER_CHATGPT_BASE_URL=" + issuer.URL + "/chatgpt/", "OPENAI_BASE_URL=" + issuer.URL + "/v1/",
		"HTTP_PROXY=" + issuer.URL, "HTTPS_PROXY=" + issuer.URL, "ALL_PROXY=" + issuer.URL,
		"NO_PROXY=127.0.0.1,localhost", "RUST_LOG=off", "DO_NOT_TRACK=1",
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = input.Close()
		cancel()
		_ = cmd.Wait()
		if t.Failed() {
			text := stderr.String()
			if len(text) > 1200 {
				text = text[len(text)-1200:]
			}
			t.Logf("isolated CLI diagnostics: %s", text)
			mu.Lock()
			t.Logf("fake token calls=%d; test certificate CA=%v", calls, issuer.Certificate().IsCA)
			mu.Unlock()
		}
	}()
	send := json.NewEncoder(input)
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	result := func(id int) json.RawMessage {
		for scanner.Scan() {
			var reply struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if json.Unmarshal(scanner.Bytes(), &reply) == nil && reply.ID == id {
				if len(reply.Error) > 0 {
					t.Logf("isolated RPC error: %s", reply.Error)
				}
				return reply.Result
			}
		}
		return nil
	}
	if err := send.Encode(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{
		"clientInfo": map[string]string{"name": "loopforge-isolated-writeback-probe", "version": "0.1.0"},
	}}); err != nil {
		t.Fatal(err)
	}
	if result(1) == nil {
		t.Fatal("isolated Codex could not initialize; check binary/sandbox support")
	}
	_ = send.Encode(map[string]string{"method": "initialized"})
	for id := 2; id <= 3; id++ {
		if err := send.Encode(map[string]any{"id": id, "method": "account/read", "params": map[string]bool{"refreshToken": true}}); err != nil {
			t.Fatal(err)
		}
		if result(id) == nil {
			t.Fatal("official Codex could not refresh against the isolated fake issuer")
		}
	}
	raw, err := os.ReadFile(filepath.Join(codexHome, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	hostRaw, readErr := os.ReadFile(source.path)
	if readErr != nil || calls != 2 || probeReadTokens("codex", hostRaw).Refresh != latest || strings.Contains(string(raw), "fake-refresh-") {
		t.Fatal("managed Codex did not keep the rotating token on the host")
	}
	t.Logf("official Codex completed %d fake refreshes; non-loopback proxy requests rejected: %d", calls, rejectedEgress)
}
