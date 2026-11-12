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
