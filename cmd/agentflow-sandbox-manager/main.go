// agentflow-sandbox-manager is the independently deployable control endpoint
// for execution-environment leases. It deliberately has no access to the
// LoopForge database or API process; a worker on the execution machine owns
// Docker and workspace operations and uses this service only for durable
// allocation/renew/release fencing.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"agentflow/internal/executionenv"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8091", "listen address")
	state := flag.String("state", "", "lease state file (or AGENTFLOW_SANDBOX_LEASE_STATE)")
	dsn := flag.String("dsn", "", "MySQL DSN for transactional lease storage (or AGENTFLOW_SANDBOX_MANAGER_DSN)")
	token := flag.String("token", "", "Bearer token (or AGENTFLOW_SANDBOX_MANAGER_TOKEN)")
	flag.Parse()
	path := *state
	if path == "" {
		path = os.Getenv("AGENTFLOW_SANDBOX_LEASE_STATE")
	}
	managerToken := *token
	if managerToken == "" {
		managerToken = os.Getenv("AGENTFLOW_SANDBOX_MANAGER_TOKEN")
	}
	databaseDSN := *dsn
	if databaseDSN == "" {
		databaseDSN = os.Getenv("AGENTFLOW_SANDBOX_MANAGER_DSN")
	}
	if strings.TrimSpace(managerToken) == "" {
		log.Fatal("sandbox manager requires an authentication token")
	}
	var store executionenv.LeaseBackend
	if strings.TrimSpace(databaseDSN) != "" {
		db, err := gorm.Open(mysql.Open(databaseDSN), &gorm.Config{})
		if err != nil {
			log.Fatal(err)
		}
		store = executionenv.NewSQLLeaseStore(db)
	} else {
		if strings.TrimSpace(path) == "" {
			log.Fatal("sandbox manager requires either a MySQL DSN or persistent state file")
		}
		fileStore, err := executionenv.OpenLeaseStore(path)
		if err != nil {
			log.Fatal(err)
		}
		store = fileStore
	}
	server := &http.Server{Addr: *addr, Handler: executionenv.Handler{Store: store, Token: managerToken}, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("sandbox manager listening on %s", *addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
