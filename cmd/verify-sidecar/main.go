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

	"agentflow/internal/critique"
	"agentflow/internal/verifysidecar"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8787", "listen address; keep it on loopback")
	root := flag.String("root", ".", "directory that bounds every repo path the sidecar may read")
	modelURL := flag.String("model-url", os.Getenv("AEGIS_MODEL_URL"), "OpenAI-compatible base URL for /critique (env AEGIS_MODEL_URL)")
	modelKey := flag.String("model-key", os.Getenv("AEGIS_MODEL_KEY"), "model API key (env AEGIS_MODEL_KEY)")
	modelKeyFile := flag.String("model-key-file", os.Getenv("AEGIS_MODEL_KEY_FILE"), "file holding the model API key (env AEGIS_MODEL_KEY_FILE)")
	modelName := flag.String("model", os.Getenv("AEGIS_MODEL_NAME"), "model name for /critique (env AEGIS_MODEL_NAME)")
	mavenCache := flag.String("m2-cache", "", "host directory mounted at /root/.m2 for repeated Maven runs")
	acceptanceImage := flag.String("acceptance-image", "", "JUnit acceptance runner image (default loopforge-verify-acceptance:local)")
	allowTestCommand := flag.Bool("allow-test-command", false, "allow /verify to run the request testCommand inside the Maven container")
	flag.Parse()

	if !strings.HasPrefix(*addr, "127.0.0.1:") && !strings.HasPrefix(*addr, "localhost:") && !strings.HasPrefix(*addr, "[::1]:") {
		log.Printf("warning: %s is not a loopback address; the sidecar runs shell commands on request", *addr)
	}
	rootAbs, err := filepath.Abs(*root)
	if err != nil {
		log.Fatal(err)
	}

	key := strings.TrimSpace(*modelKey)
	if key == "" && strings.TrimSpace(*modelKeyFile) != "" {
		raw, err := os.ReadFile(*modelKeyFile)
		if err != nil {
			log.Fatalf("read model key file: %v", err)
		}
		key = strings.TrimSpace(string(raw))
	}
	sidecar := verifysidecar.New(rootAbs)
	sidecar.Critique = critique.Config{URL: strings.TrimSpace(*modelURL), Key: key, Model: strings.TrimSpace(*modelName)}
	sidecar.AllowTestCommand = *allowTestCommand
	sidecar.MavenCacheDir = strings.TrimSpace(*mavenCache)
	sidecar.AcceptanceImage = strings.TrimSpace(*acceptanceImage)
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
