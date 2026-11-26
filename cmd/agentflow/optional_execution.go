package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/managedagent"
	"agentflow/internal/opensandbox"
)

func configureOptionalExecution(e *engine.Engine) error {
	if project := strings.TrimSpace(os.Getenv("AGENTFLOW_WMA_PROJECT")); project != "" {
		userID, err := strconv.ParseInt(os.Getenv("AGENTFLOW_WMA_USER_ID"), 10, 64)
		if err != nil || userID <= 0 {
			return errors.New("AGENTFLOW_WMA_USER_ID must be a positive LoopForge user ID")
		}
		config := managedagent.Config{
			Endpoint:     os.Getenv("AGENTFLOW_WMA_ENDPOINT"),
			EnterpriseID: os.Getenv("AGENTFLOW_WMA_ENTERPRISE_ID"),
			AgentID:      os.Getenv("AGENTFLOW_WMA_AGENT_ID"),
			SessionID:    os.Getenv("AGENTFLOW_WMA_SESSION_ID"),
			APIKey:       os.Getenv("AGENTFLOW_WMA_API_KEY"),
			ACPOrigin:    os.Getenv("AGENTFLOW_WMA_ACP_ORIGIN"),
		}
		secretID, secretKey := os.Getenv("AGENTFLOW_WMA_SECRET_ID"), os.Getenv("AGENTFLOW_WMA_SECRET_KEY")
		if secretID != "" || secretKey != "" {
			config.TencentCloud = &managedagent.TencentCloudConfig{
				SecretID: secretID, SecretKey: secretKey,
				Token: os.Getenv("AGENTFLOW_WMA_SECURITY_TOKEN"), Region: os.Getenv("AGENTFLOW_WMA_REGION"),
			}
		}
		client, err := managedagent.NewWMA(config, nil)
		if err != nil {
			return err
		}
		if err := e.ConfigureManagedAgent(domain.WMAExecutorID, project, userID, client); err != nil {
			return err
		}
	}
	if file := strings.TrimSpace(os.Getenv("AGENTFLOW_A2A_BINDINGS_FILE")); file != "" {
		if err := configureA2ABindings(e, file); err != nil {
			return err
		}
	}
	if path := strings.TrimSpace(os.Getenv("AGENTFLOW_OPENSANDBOX_BINDINGS_FILE")); path != "" {
		if err := configureOpenSandboxBackends(e, path); err != nil {
			return err
		}
	}
	if projects := engine.ParseCloudRunnerProjects(os.Getenv("AGENTFLOW_OPENSANDBOX_PROJECTS")); len(projects) > 0 {
		client, err := opensandbox.New(opensandbox.Config{
			Endpoint:         os.Getenv("AGENTFLOW_OPENSANDBOX_ENDPOINT"),
			APIKey:           os.Getenv("AGENTFLOW_OPENSANDBOX_API_KEY"),
			RunnerURL:        os.Getenv("AGENTFLOW_OPENSANDBOX_RUNNER_URL"),
			RunnerImage:      os.Getenv("AGENTFLOW_OPENSANDBOX_RUNNER_IMAGE"),
			Scope:            os.Getenv("AGENTFLOW_OPENSANDBOX_SCOPE"),
			RunnerSigningKey: os.Getenv("AGENTFLOW_OPENSANDBOX_RUNNER_SIGNING_KEY"),
		}, nil)
		if err != nil {
			return err
		}
		if err := e.ConfigureOpenSandbox(engine.DefaultOpenSandboxBackend, client, projects); err != nil {
			return err
		}
	}
	return nil
}

func readExecutionBindings(path string, bindings any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(bindings); err != nil || decoder.Decode(new(json.RawMessage)) != io.EOF {
		return errors.New("expected one JSON array with the supported fields")
	}
	return nil
}

func configureOpenSandboxBackends(e *engine.Engine, path string) error {
	var bindings []struct {
		ID         string   `json:"id"`
		ProjectIDs []string `json:"projectIds"`
		opensandbox.Config
	}
	if err := readExecutionBindings(path, &bindings); err != nil {
		return fmt.Errorf("OpenSandbox bindings: %w", err)
	}
	for _, binding := range bindings {
		client, err := opensandbox.New(binding.Config, nil)
		if err != nil {
			return fmt.Errorf("OpenSandbox backend %q: %w", binding.ID, err)
		}
		if err := e.ConfigureOpenSandbox(binding.ID, client, binding.ProjectIDs); err != nil {
			return err
		}
	}
	return nil
}

func configureA2ABindings(e *engine.Engine, path string) error {
	var bindings []struct {
		ProjectID         string `json:"projectId"`
		ResponsibleUserID int64  `json:"responsibleUserId"`
		Endpoint          string `json:"endpoint"`
		Token             string `json:"token,omitempty"`
	}
	if err := readExecutionBindings(path, &bindings); err != nil {
		return fmt.Errorf("A2A bindings: %w", err)
	}
	for i, binding := range bindings {
		client, err := managedagent.NewA2A(managedagent.A2AConfig{Endpoint: binding.Endpoint, Token: binding.Token}, nil)
		if err != nil {
			return fmt.Errorf("A2A binding %d: %w", i+1, err)
		}
		if err := e.ConfigureManagedAgent(domain.A2AExecutorID, binding.ProjectID, binding.ResponsibleUserID, client); err != nil {
			return err
		}
	}
	return nil
}
