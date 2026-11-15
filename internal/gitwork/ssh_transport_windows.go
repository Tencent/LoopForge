package gitwork

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

// Git for Windows bundles MSYS OpenSSH, whose AF_LOCAL sockets differ from
// native Windows AF_UNIX and the system OpenSSH service's named pipe. Select
// that client explicitly so a user's PATH cannot change the agent transport.
func sshClientCommand(clientRoot string) (string, error) {
	if clientRoot != "" {
		return "ssh", nil // the client executes inside a Unix sandbox
	}
	git, err := exec.LookPath("git.exe")
	if err == nil {
		root := filepath.Dir(filepath.Dir(git))
		for _, candidate := range []string{
			filepath.Join(root, "usr", "bin", "ssh.exe"),
			filepath.Join(filepath.Dir(root), "usr", "bin", "ssh.exe"),
		} {
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				return shellQuote(filepath.ToSlash(candidate)), nil
			}
		}
	}
	return "", fmt.Errorf("Windows personal Git SSH keys require Git for Windows with its bundled OpenSSH client")
}

// msysSSHListener implements MSYS AF_LOCAL's cookie-authenticated loopback
// transport. Protocol reference (no upstream implementation is copied):
// https://github.com/msys2/msys2-runtime/blob/master/winsup/cygwin/fhandler_socket_local.cc
// The filesystem holds public SSH config and a random socket cookie, never a
// private key. Restrict the directory to the Runner's Windows account first.
type msysSSHListener struct {
	net.Listener
	secret  [16]byte
	mu      sync.Mutex
	closed  bool
	pending map[net.Conn]struct{}
}

func listenSSHAgent(dir string) (net.Listener, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return nil, err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return nil, err
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return nil, fmt.Errorf("restrict Git SSH agent directory: %w", err)
	}
	raw, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	listener := &msysSSHListener{Listener: raw, pending: map[net.Conn]struct{}{}}
	if _, err := rand.Read(listener.secret[:]); err != nil {
		raw.Close()
		return nil, err
	}
	words := [4]uint32{}
	for i := range words {
		words[i] = binary.LittleEndian.Uint32(listener.secret[i*4:])
	}
	content := fmt.Sprintf("!<socket >%d s %08x-%08x-%08x-%08x\x00", raw.Addr().(*net.TCPAddr).Port, words[0], words[1], words[2], words[3])
	path := filepath.Join(dir, "agent")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		raw.Close()
		return nil, err
	}
	pathUTF16, err := windows.UTF16PtrFromString(path)
	if err == nil {
		err = windows.SetFileAttributes(pathUTF16, windows.FILE_ATTRIBUTE_SYSTEM)
	}
	if err != nil {
		raw.Close()
		os.Remove(path)
		return nil, err
	}
	return listener, nil
}

func (l *msysSSHListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			conn.Close()
			return nil, net.ErrClosed
		}
		l.pending[conn] = struct{}{}
		l.mu.Unlock()
		err = l.handshake(conn)
		l.mu.Lock()
		delete(l.pending, conn)
		l.mu.Unlock()
		if err == nil {
			return conn, nil
		}
		conn.Close()
	}
}

func (l *msysSSHListener) handshake(conn net.Conn) error {
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	var secret [16]byte
	if _, err := io.ReadFull(conn, secret[:]); err != nil {
		return err
	}
	if subtle.ConstantTimeCompare(secret[:], l.secret[:]) != 1 {
		return fmt.Errorf("invalid Git SSH socket cookie")
	}
	if _, err := conn.Write(l.secret[:]); err != nil {
		return err
	}
	// The emulation layer exchanges pid/uid/gid after the cookie. Echo the
	// client's account IDs; authorization already used the owner-only cookie.
	var credentials [12]byte
	if _, err := io.ReadFull(conn, credentials[:]); err != nil {
		return err
	}
	binary.LittleEndian.PutUint32(credentials[:4], uint32(os.Getpid()))
	if _, err := conn.Write(credentials[:]); err != nil {
		return err
	}
	return conn.SetDeadline(time.Time{})
}

func (l *msysSSHListener) Close() error {
	l.mu.Lock()
	l.closed = true
	for conn := range l.pending {
		conn.Close()
	}
	l.mu.Unlock()
	return l.Listener.Close()
}
