package main

import (
	"log"
	"os"

	"agentflow/internal/gatemcp"
)

func main() {
	if err := gatemcp.Run(os.Stdin, os.Stdout, os.Getenv("AGENTFLOW_GATE_RESULT_PATH")); err != nil {
		log.Fatal(err)
	}
}
