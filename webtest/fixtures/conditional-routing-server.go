// A local, in-memory test server for conditional-routing-integration.test.mjs.
// It is not a production server and listens only on the loopback interface.
package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"

	"agentflow/internal/engine"
	"agentflow/internal/httpapi"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func main() {
	os.Setenv("AGENTFLOW_AUTH_REQUIRED", "false")
	os.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "false")
	s, err := repo.OpenTest()
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()
	e := engine.New(s, runtime.NewManager(filepath.Join(".local", "issue354-simulation", "workspaces")))
	router := httpapi.New(s, e, tracker.NewService(), "", "")
	log.Fatal(http.ListenAndServe("127.0.0.1:39154", router))
}
