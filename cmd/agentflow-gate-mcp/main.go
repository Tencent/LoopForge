package main

import (
	"log"
	"os"

	"agentflow/internal/gatemcp"
	"agentflow/internal/scriptview"
)

func main() {
	if handled, err := scriptview.Run(os.Args[1:]); handled {
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := gatemcp.Run(os.Stdin, os.Stdout, os.Getenv("AGENTFLOW_GATE_RESULT_PATH")); err != nil {
		log.Fatal(err)
	}
}
