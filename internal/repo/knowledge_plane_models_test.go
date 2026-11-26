package repo

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// 主库与内容库的增量 DDL 必须按面拆分：主库不能建内容表，内容库也不能漏掉
// staging；needsUTF8MB4 要覆盖 049/050/052 新增的三张控制面表。
func TestKnowledgeModelListsSplitByPlane(t *testing.T) {
	content := map[reflect.Type]bool{
		reflect.TypeOf(knowledgeContentFenceRow{}):      true,
		reflect.TypeOf(knowledgeDocumentRow{}):          true,
		reflect.TypeOf(knowledgeRevisionRow{}):          true,
		reflect.TypeOf(knowledgeChunkRow{}):             true,
		reflect.TypeOf(knowledgeSubmissionContentRow{}): true,
	}
	controlSet := map[reflect.Type]bool{}
	for _, model := range controlModels() {
		typ := reflect.TypeOf(model)
		if content[typ] {
			t.Fatalf("内容表 %s 不能出现在主库模型列表", typ)
		}
		controlSet[typ] = true
	}
	for typ := range content {
		if controlSet[typ] {
			t.Fatalf("内容表 %s 同时出现在主库列表", typ)
		}
	}
	contentSet := map[reflect.Type]bool{}
	for _, model := range contentModels() {
		typ := reflect.TypeOf(model)
		contentSet[typ] = true
	}
	for typ := range content {
		if !contentSet[typ] {
			t.Fatalf("内容库模型列表缺少 %s", typ)
		}
	}
	if len(allModels()) != len(controlModels())+len(contentModels()) {
		t.Fatalf("allModels 不是两面的并集")
	}

	for _, model := range []any{knowledgeWritebackRow{}, knowledgeTombstoneRow{}, knowledgeCitationRow{}, knowledgeSubmissionContentRow{}} {
		if !needsUTF8MB4Table(model) {
			t.Fatalf("%T 未加入 needsUTF8MB4Table", model)
		}
	}
}

// 044/051/054 同时含控制段与内容段；054 必须带 staging、二进制路径 collation
// 和写回可空键。脚本由 knowledge-schema.sh 按标记分库执行。
func TestKnowledgeMigrationScriptsSplitPlanes(t *testing.T) {
	for _, name := range []string{"044_builtin_knowledge.sql", "051_knowledge_governance.sql", "053_knowledge_concurrency.sql", "054_knowledge_schema_cleanup.sql"} {
		raw, err := os.ReadFile("../../migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		script := string(raw)
		if !strings.Contains(script, "-- @plane control") || !strings.Contains(script, "-- @plane content") {
			t.Fatalf("%s 缺少控制面/内容面标记", name)
		}
	}
	raw, err := os.ReadFile("../../migrations/054_knowledge_schema_cleanup.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"knowledge_submission_contents", "utf8mb4_bin", "submission_id = NULL", "knowledge_control_schema"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("054 缺少 %q", want)
		}
	}
}
