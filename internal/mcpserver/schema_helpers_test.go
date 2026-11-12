package mcpserver

import (
	"agentflow/internal/application"
	"reflect"
	"testing"
)

func TestContractOperationMatchesRenamedPathParameter(t *testing.T) {
	ToolInputSchema(application.Operation{}) // load the contract
	route, method := contractOperation(application.Operation{Method: "GET", Path: "/api/assistant/tasks"})
	if route == nil || method == nil {
		t.Fatalf("documented route not found: route=%v method=%v", route != nil, method != nil)
	}
	if got := method["operationId"]; got != "listAssistantTasks" {
		t.Fatalf("operationId = %v", got)
	}
	route, method = contractOperation(application.Operation{Method: "GET", Path: "/api/no-such-route/{id}"})
	if route != nil || method != nil {
		t.Fatalf("undocumented route matched: %v %v", route, method)
	}
}

func TestQueryParameterSchemaMergesRouteAndMethodParameters(t *testing.T) {
	route := map[string]any{"parameters": []any{
		map[string]any{"name": "path", "in": "query", "required": true},
		map[string]any{"name": "id", "in": "path", "required": true},
	}}
	method := map[string]any{"parameters": []any{
		map[string]any{"name": "limit", "in": "query", "description": "page size"},
		map[string]any{"name": "", "in": "query", "required": true},
	}}
	fields, required := queryParameterSchema(route, method)
	want := map[string]any{
		"path":  map[string]any{"type": "string"},
		"limit": map[string]any{"type": "string", "description": "page size"},
	}
	if !reflect.DeepEqual(fields, want) {
		t.Fatalf("fields = %#v", fields)
	}
	if !reflect.DeepEqual(required, []string{"path"}) {
		t.Fatalf("required = %#v", required)
	}
}

func TestQueryParameterSchemaWithoutContract(t *testing.T) {
	fields, required := queryParameterSchema(nil, nil)
	if len(fields) != 0 || len(required) != 0 {
		t.Fatalf("fields=%v required=%v", fields, required)
	}
}

func TestContractOperationIgnoresPathParameterNames(t *testing.T) {
	ToolInputSchema(application.Operation{}) // load the contract
	// The contract documents /api/issues/{issueId}; manifests may use {id}.
	route, method := contractOperation(application.Operation{Method: "GET", Path: "/api/issues/{id}"})
	if route == nil || method == nil {
		t.Fatalf("renamed path parameter not matched: route=%v method=%v", route != nil, method != nil)
	}
	if got := method["operationId"]; got != "getIssue" {
		t.Fatalf("operationId = %v", got)
	}
	if _, ok := route["parameters"].([]any); !ok {
		t.Fatalf("route-level parameters missing: %#v", route["parameters"])
	}
}

func TestContractOperationUndocumentedMethod(t *testing.T) {
	ToolInputSchema(application.Operation{}) // load the contract
	// The route exists but has no PATCH operation: keep the route, drop the method.
	route, method := contractOperation(application.Operation{Method: "PATCH", Path: "/api/issues/{id}"})
	if route == nil {
		t.Fatal("documented route not found")
	}
	if method != nil {
		t.Fatalf("undocumented method matched: %#v", method)
	}
}

func TestQueryParameterSchemaResolvesContractRefs(t *testing.T) {
	ToolInputSchema(application.Operation{}) // load the contract
	// deleteRepository declares its required id query parameter through $ref.
	route, method := contractOperation(application.Operation{Method: "DELETE", Path: "/api/repositories"})
	fields, required := queryParameterSchema(route, method)
	if !reflect.DeepEqual(fields, map[string]any{"id": map[string]any{"type": "string"}}) {
		t.Fatalf("fields = %#v", fields)
	}
	if !reflect.DeepEqual(required, []string{"id"}) {
		t.Fatalf("required = %#v", required)
	}
	// Path parameters referenced at the route level must not leak into query.
	route, method = contractOperation(application.Operation{Method: "GET", Path: "/api/issues/{id}/runs"})
	fields, required = queryParameterSchema(route, method)
	if len(fields) != 0 || len(required) != 0 {
		t.Fatalf("path parameter leaked into query: fields=%v required=%v", fields, required)
	}
}

func TestToolInputSchemaRequiresRefQueryParameter(t *testing.T) {
	op := application.Operation{Method: "DELETE", Path: "/api/repositories"}
	schema := ToolInputSchema(op)
	if !reflect.DeepEqual(schema["required"], []string{"query"}) {
		t.Fatalf("required = %#v", schema["required"])
	}
	if err := ValidateToolInput(op, map[string]any{}); err == nil {
		t.Fatal("missing required query parameter accepted")
	}
	if err := ValidateToolInput(op, map[string]any{"query": map[string]any{"id": "repo-1"}}); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
}
