package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"agentflow/internal/managedagent"
)

func configuredExternalAgent() (managedagent.TextAgent, error) {
	config := managedagent.A2AConfig{
		Endpoint: strings.TrimSpace(os.Getenv("AGENTFLOW_RUNNER_A2A_ENDPOINT")),
		Token:    os.Getenv("AGENTFLOW_RUNNER_A2A_TOKEN"),
		Loopback: true,
	}
	if config.Endpoint != "" {
		return managedagent.NewA2A(config, nil)
	}
	path := os.Getenv("AGENTFLOW_RUNNER_A2A_CONFIG")
	implicit := path == ""
	if implicit {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil
		}
		path = filepath.Join(home, ".agentflow-runner", "a2a.json")
	}
	file, err := os.Open(path)
	if implicit && errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var local struct {
		Endpoint string `json:"endpoint"`
		Token    string `json:"token,omitempty"`
	}
	decoder := json.NewDecoder(io.LimitReader(file, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&local); err != nil || decoder.Decode(new(json.RawMessage)) != io.EOF {
		return nil, errors.New("invalid Runner A2A configuration")
	}
	config.Endpoint, config.Token = local.Endpoint, local.Token
	return managedagent.NewA2A(config, nil)
}
