// Command verify-sidecar runs the same-host verification sidecar LoopForge SOP
// nodes call. See docs/enhancements/verify-sidecar.md for the contract.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"agentflow/internal/verifysidecar"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8787", "listen address; keep it on loopback")
	root := flag.String("root", ".", "directory that bounds every repo path the sidecar may read")
	critiqueFile := flag.String("critique-file", "", "JSON object mapping diffHash to {verdict, summary}; absent hashes are REJECT")
	allowTestCommand := flag.Bool("allow-test-command", false, "allow /verify to run the request testCommand inside the Maven container")
	flag.Parse()

	if !strings.HasPrefix(*addr, "127.0.0.1:") && !strings.HasPrefix(*addr, "localhost:") && !strings.HasPrefix(*addr, "[::1]:") {
		log.Printf("warning: %s is not a loopback address; the sidecar runs shell commands on request", *addr)
	}
	rootAbs, err := filepath.Abs(*root)
	if err != nil {
		log.Fatal(err)
	}

	sidecar := verifysidecar.New(rootAbs, *critiqueFile)
	sidecar.AllowTestCommand = *allowTestCommand
	server := &http.Server{
		Addr:              *addr,
		Handler:           sidecar.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	log.Printf("verify-sidecar listening on %s (root %s, test commands %v)", *addr, rootAbs, *allowTestCommand)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
