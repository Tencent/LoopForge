package dockerproxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/http2"
	"google.golang.org/protobuf/encoding/protowire"
)

// buildx drives the daemon's BuildKit over /grpc: an HTTP upgrade to h2c,
// then gRPC. The proxy terminates HTTP/2 on both sides so it can read each
// call. Control.Solve is the one call that decides what a build may do and
// where its image goes; it is checked and labelled like a classic /build.
// Everything else is relayed as is, except calls that act on the whole
// daemon (cache prune, other builds' history) and unknown services.

const grpcSolve = "/moby.buildkit.v1.Control/Solve"

var grpcAllowed = map[string]bool{
	"/moby.buildkit.v1.Control/Solve":              true,
	"/moby.buildkit.v1.Control/Status":             true,
	"/moby.buildkit.v1.Control/Session":            true,
	"/moby.buildkit.v1.Control/ListWorkers":        true,
	"/moby.buildkit.v1.Control/Info":               true,
	"/moby.buildkit.v1.Control/DiskUsage":          true,
	"/moby.buildkit.v1.Control/ListenBuildHistory": true,
	"/grpc.health.v1.Health/Check":                 true,
}

func grpcMethodAllowed(method string) bool {
	if grpcAllowed[method] {
		return true
	}
	// Client-side frontends (docker buildx build with a gateway frontend)
	// solve through the LLB bridge; its builds stay bound to the checked
	// Control.Solve (entitlements, exporter).
	return strings.HasPrefix(method, "/moby.buildkit.v1.frontend.LLBBridge/")
}

// serveGRPC upgrades the client connection and relays gRPC calls to the
// daemon's /grpc endpoint, checking them on the way.
func (p *Proxy) serveGRPC(w http.ResponseWriter, r *http.Request) {
	upstream, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(r.Context(), "unix", p.cfg.Upstream)
	if err != nil {
		deny(w, http.StatusBadGateway, "Docker 守护进程不可用："+err.Error())
		return
	}
	out := r.Clone(context.Background())
	out.Host = "docker"
	out.RequestURI = ""
	out.URL = &url.URL{Path: r.URL.Path, RawQuery: r.URL.RawQuery}
	out.Body = http.NoBody
	out.ContentLength = 0
	if err := out.Write(upstream); err != nil {
		_ = upstream.Close()
		deny(w, http.StatusBadGateway, err.Error())
		return
	}
	upstreamReader := bufio.NewReader(upstream)
	resp, err := http.ReadResponse(upstreamReader, out)
	if err != nil {
		_ = upstream.Close()
		deny(w, http.StatusBadGateway, err.Error())
		return
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		defer upstream.Close()
		defer resp.Body.Close()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return
	}
	client, clientRW, err := http.NewResponseController(w).Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	defer client.Close()
	defer upstream.Close()
	_, _ = clientRW.WriteString("HTTP/1.1 101 UPGRADED\r\nConnection: Upgrade\r\nUpgrade: h2c\r\n\r\n")
	if err := clientRW.Flush(); err != nil {
		return
	}
	transport := &http2.Transport{AllowHTTP: true}
	cc, err := transport.NewClientConn(&bufferedConn{Conn: upstream, r: upstreamReader})
	if err != nil {
		return
	}
	defer cc.Close()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { p.relayGRPC(w, r, cc) })
	quiet := &http.Server{ErrorLog: log.New(io.Discard, "", 0)} // buildx probes and drops connections
	(&http2.Server{}).ServeConn(&bufferedConn{Conn: client, r: clientRW.Reader}, &http2.ServeConnOpts{Context: r.Context(), Handler: handler, BaseConfig: quiet})
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) { return c.r.Read(b) }

func (p *Proxy) relayGRPC(w http.ResponseWriter, r *http.Request, cc *http2.ClientConn) {
	method := r.URL.Path
	if !grpcMethodAllowed(method) {
		grpcDeny(w, "工作区不能调用 BuildKit "+method)
		return
	}
	body := io.Reader(r.Body)
	if method == grpcSolve {
		message, err := readGRPCMessage(r.Body)
		if err != nil {
			grpcDeny(w, "无法解析构建请求："+err.Error())
			return
		}
		checked, err := p.checkSolve(r.Context(), message)
		if err != nil {
			grpcDeny(w, err.Error())
			return
		}
		body = bytes.NewReader(frameGRPC(checked))
	}
	out, err := http.NewRequestWithContext(r.Context(), r.Method, "http://docker"+r.URL.RequestURI(), body)
	if err != nil {
		grpcDeny(w, err.Error())
		return
	}
	out.Header = r.Header.Clone()
	out.Trailer = r.Trailer
	out.ContentLength = -1
	resp, err := cc.RoundTrip(out)
	if err != nil {
		grpcDeny(w, "BuildKit 不可用："+err.Error())
		return
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	flusher := http.NewResponseController(w)
	_ = flusher.Flush()
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			_ = flusher.Flush()
		}
		if err != nil {
			break
		}
	}
	for k, v := range resp.Trailer {
		for _, value := range v {
			w.Header().Add(http.TrailerPrefix+k, value)
		}
	}
}

// grpcDeny answers a call with PERMISSION_DENIED (trailers-only response).
func grpcDeny(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/grpc")
	w.Header().Set("Grpc-Status", "7")
	w.Header().Set("Grpc-Message", grpcEncodeMessage("LoopForge 工作区 Docker 代理："+message))
	w.WriteHeader(http.StatusOK)
}

// grpcEncodeMessage percent-encodes as the gRPC spec requires for
// grpc-message.
func grpcEncodeMessage(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 0x20 && c <= 0x7e && c != '%' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func readGRPCMessage(r io.Reader) ([]byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	if header[0] != 0 {
		return nil, fmt.Errorf("不支持压缩的 gRPC 消息")
	}
	size := binary.BigEndian.Uint32(header[1:])
	if size > 64<<20 {
		return nil, fmt.Errorf("构建请求过大")
	}
	message := make([]byte, size)
	if _, err := io.ReadFull(r, message); err != nil {
		return nil, err
	}
	return message, nil
}

func frameGRPC(message []byte) []byte {
	out := make([]byte, 5+len(message))
	binary.BigEndian.PutUint32(out[1:], uint32(len(message)))
	copy(out[5:], message)
	return out
}

// SolveRequest field numbers (moby/buildkit api/services/control).
const (
	solveExporterDeprecated      = 3
	solveExporterAttrsDeprecated = 4
	solveFrontendAttrs           = 7
	solveEntitlements            = 9
	solveExporters               = 13
	exporterType                 = 1
	exporterAttrs                = 2
	mapKey                       = 1
	mapValue                     = 2
)

// checkSolve applies the classic /build rules to a BuildKit solve: no
// entitlements (host network, insecure, devices), no host network or cgroup
// parent through frontend options, image names only where /build -t may
// write; and labels the image (Dockerfile frontend) with the workspace.
func (p *Proxy) checkSolve(ctx context.Context, message []byte) ([]byte, error) {
	var exporterDeprecated string
	exporterAttrsDeprecated := map[string]string{}
	frontendAttrs := map[string]string{}
	type exporter struct {
		kind  string
		attrs map[string]string
	}
	var exporters []exporter
	for b := message; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		b = b[n:]
		if typ != protowire.BytesType {
			n = protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			b = b[n:]
			continue
		}
		value, n := protowire.ConsumeBytes(b)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		b = b[n:]
		switch num {
		case solveExporterDeprecated:
			exporterDeprecated = string(value)
		case solveExporterAttrsDeprecated:
			if err := parseMapEntry(value, exporterAttrsDeprecated); err != nil {
				return nil, err
			}
		case solveFrontendAttrs:
			if err := parseMapEntry(value, frontendAttrs); err != nil {
				return nil, err
			}
		case solveEntitlements:
			return nil, forbidden("构建不能申请特权（--allow " + string(value) + "）")
		case solveExporters:
			e := exporter{attrs: map[string]string{}}
			for v := value; len(v) > 0; {
				fnum, ftyp, m := protowire.ConsumeTag(v)
				if m < 0 || ftyp != protowire.BytesType {
					return nil, fmt.Errorf("exporter 字段无效")
				}
				v = v[m:]
				field, m := protowire.ConsumeBytes(v)
				if m < 0 {
					return nil, protowire.ParseError(m)
				}
				v = v[m:]
				switch fnum {
				case exporterType:
					e.kind = string(field)
				case exporterAttrs:
					if err := parseMapEntry(field, e.attrs); err != nil {
						return nil, err
					}
				}
			}
			exporters = append(exporters, e)
		}
	}
	if exporterDeprecated != "" {
		exporters = append(exporters, exporter{kind: exporterDeprecated, attrs: exporterAttrsDeprecated})
	}
	switch mode := strings.ToLower(frontendAttrs["force-network-mode"]); mode {
	case "", "default", "sandbox", "none":
	default:
		return nil, forbidden("构建只能用默认网络或 none（--network " + mode + "）")
	}
	if frontendAttrs["cgroup-parent"] != "" {
		return nil, forbidden("构建不能指定 --cgroup-parent")
	}
	for _, e := range exporters {
		switch e.kind {
		case "moby", "image", "docker":
			for _, name := range strings.Split(e.attrs["name"], ",") {
				if err := p.mayWriteTag(ctx, strings.TrimSpace(name)); err != nil {
					return nil, err
				}
			}
		}
	}
	// A later map entry wins, so the workspace label cannot be overridden.
	entry := protowire.AppendTag(nil, mapKey, protowire.BytesType)
	entry = protowire.AppendString(entry, "label:"+Label)
	entry = protowire.AppendTag(entry, mapValue, protowire.BytesType)
	entry = protowire.AppendString(entry, p.cfg.Workspace)
	out := append([]byte(nil), message...)
	out = protowire.AppendTag(out, solveFrontendAttrs, protowire.BytesType)
	return protowire.AppendBytes(out, entry), nil
}

func parseMapEntry(b []byte, into map[string]string) error {
	var key, value string
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 || typ != protowire.BytesType {
			return fmt.Errorf("map 字段无效")
		}
		b = b[n:]
		field, n := protowire.ConsumeBytes(b)
		if n < 0 {
			return protowire.ParseError(n)
		}
		b = b[n:]
		switch num {
		case mapKey:
			key = string(field)
		case mapValue:
			value = string(field)
		}
	}
	into[key] = value
	return nil
}
