package workspacessh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestSandboxSSHForwardsOnlyToLoopback(t *testing.T) {
	inRead, inWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inWrite.Close()
	outRead, outWrite, err := os.Pipe()
	if err != nil {
		_ = inRead.Close()
		t.Fatal(err)
	}
	defer outRead.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	seed := HostSeed("test-secret", "workspace")
	host, err := hostSigner(seed)
	if err != nil {
		t.Fatal(err)
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientKey, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	done := make(chan error, 1)
	go func() {
		defer inRead.Close()
		defer outWrite.Close()
		done <- ServeSandbox(ctx, inRead, outWrite, workspace)
	}()
	if err := json.NewEncoder(inWrite).Encode(Keys{HostSeed: seed, ClientKey: clientKey.PublicKey().Marshal()}); err != nil {
		t.Fatal(err)
	}
	var ready [1]byte
	if _, err := io.ReadFull(outRead, ready[:]); err != nil || ready[0] != SandboxReady {
		t.Fatalf("server readiness: %v %v", ready, err)
	}
	conn, channels, requests, err := ssh.NewClientConn(&stdioConn{Reader: outRead, Writer: inWrite, input: outRead}, "workspace", &ssh.ClientConfig{
		User: "loopforge", Auth: []ssh.AuthMethod{ssh.PublicKeys(clientKey)}, HostKeyCallback: ssh.FixedHostKey(host.PublicKey()),
	})
	if err != nil {
		t.Fatal(err)
	}
	client := ssh.NewClient(conn, channels, requests)
	defer client.Close()

	if _, err := client.Dial("tcp", "192.0.2.1:80"); err == nil {
		t.Fatal("non-loopback destination accepted")
	} else {
		var refusal *ssh.OpenChannelError
		if !errors.As(err, &refusal) || refusal.Reason != ssh.Prohibited {
			t.Fatalf("non-loopback destination was not rejected by policy: %v", err)
		}
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		connection, err := listener.Accept()
		if err == nil {
			defer connection.Close()
			_, _ = io.WriteString(connection, "remote-server")
		}
	}()
	tunnel, err := client.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	contents, err := io.ReadAll(tunnel)
	_ = tunnel.Close()
	if err != nil || string(contents) != "remote-server" {
		t.Fatalf("forwarded response: %q %v", contents, err)
	}
	_ = client.Close()
	_ = inWrite.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("sandbox SSH helper did not stop")
	}
}
