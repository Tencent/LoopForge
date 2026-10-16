package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"

	"agentflow/internal/mcpserver"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := mcpserver.ServeRemoteStdio(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

func init() {
	if os.Getenv("AGENTFLOW_API_URL") == "" {
		fmt.Fprintln(os.Stderr, "agentflow-mcp: set AGENTFLOW_API_URL to the running AgentFlow server")
	}
}
