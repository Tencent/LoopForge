package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"agentflow/internal/gatemcp"
)

func main() {
	listen := flag.String("listen", "", "HTTP listen address for remote gate submissions")
	flag.Parse()
	if *listen != "" {
		handler, err := gateHTTPHandler()
		if err != nil {
			log.Fatal(err)
		}
		server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
		log.Fatal(server.ListenAndServe())
	}
	if err := gatemcp.Run(os.Stdin, os.Stdout, os.Getenv("AGENTFLOW_GATE_RESULT_PATH")); err != nil {
		log.Fatal(err)
	}
}

func gateHTTPHandler() (http.Handler, error) {
	connectorToken := os.Getenv("AGENTFLOW_GATE_MCP_CONNECTOR_TOKEN")
	if path := strings.TrimSpace(os.Getenv("AGENTFLOW_GATE_MCP_SIGNING_KEY_FILE")); path != "" {
		key, err := gatemcp.OpenSigningKey(path)
		if err != nil {
			return nil, err
		}
		ticketKey, err := gatemcp.OpenTicketKey()
		if err != nil {
			return nil, err
		}
		return gatemcp.NewSignedHTTP(connectorToken, key, ticketKey)
	}
	return gatemcp.NewHTTP(os.Getenv("AGENTFLOW_GATE_MCP_CONTROL_TOKEN"), connectorToken)
}
