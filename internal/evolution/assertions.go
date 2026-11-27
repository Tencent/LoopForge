package evolution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// ActiveSOPAssertions returns only enabled assertions.
func ActiveSOPAssertions(assertions []domain.EvoSOPAssertion) []domain.EvoSOPAssertion {
	out := make([]domain.EvoSOPAssertion, 0, len(assertions))
	for _, a := range assertions {
		if a.Enabled && strings.TrimSpace(a.Kind) != "" {
			out = append(out, a)
		}
	}
	return out
}

// ValidSOPAssertionKind reports whether kind is a supported Layer 2 assertion kind.
func ValidSOPAssertionKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case domain.EvoAssertArtifactExists,
		domain.EvoAssertJSONValid,
		domain.EvoAssertJSONField,
		domain.EvoAssertOutputRegex,
		domain.EvoAssertGateVerdict,
		domain.EvoAssertCommandExit:
		return true
	default:
		return false
	}
}

// ValidateSOPAssertion normalizes and validates a single Layer 2 assertion.
func ValidateSOPAssertion(a domain.EvoSOPAssertion) (domain.EvoSOPAssertion, error) {
	a.Kind = strings.TrimSpace(a.Kind)
	if !ValidSOPAssertionKind(a.Kind) {
		return a, fmt.Errorf("不支持的验收规则类型：%q", a.Kind)
	}
	a.Name = strings.TrimSpace(a.Name)
	a.TargetScope = strings.TrimSpace(a.TargetScope)
	a.Path = strings.TrimSpace(a.Path)
	a.FieldPath = strings.TrimSpace(a.FieldPath)
	a.ExpectedValue = strings.TrimSpace(a.ExpectedValue)
	a.Regex = strings.TrimSpace(a.Regex)
	a.SampleRunID = strings.TrimSpace(a.SampleRunID)
	a.Prompt = strings.TrimSpace(a.Prompt)
	a.RequiredFields = cleanStringSlice(a.RequiredFields)
	a.AllowedValues = cleanStringSlice(a.AllowedValues)
	a.Contains = cleanStringSlice(a.Contains)

	if a.Regex != "" {
		if _, err := regexp.Compile(a.Regex); err != nil {
			return a, fmt.Errorf("正则表达式无效：%v", err)
		}
	}

	switch a.Kind {
	case domain.EvoAssertArtifactExists:
		if a.Path == "" {
			return a, errors.New("产物存在检查需要指定产物文件名或路径")
		}
	case domain.EvoAssertJSONField:
		if a.FieldPath == "" {
			return a, errors.New("JSON 字段检查需要指定字段路径（如 route）")
		}
		if len(a.AllowedValues) == 0 && a.ExpectedValue == "" && a.Regex == "" {
			return a, errors.New("JSON 字段检查需要指定允许值、期望值或正则之一")
		}
	case domain.EvoAssertOutputRegex:
		if a.Regex == "" && len(a.Contains) == 0 {
			return a, errors.New("内容检查需要指定关键字或正则表达式")
		}
	case domain.EvoAssertCommandExit:
		if a.ExpectedValue == "" {
			a.ExpectedValue = "0"
		}
	case domain.EvoAssertGateVerdict:
		if a.ExpectedValue == "" {
			a.ExpectedValue = "pass"
		}
	}

	a.Expression = FormatAssertionExpression(a)
	if a.Name == "" {
		a.Name = defaultAssertionName(a)
	}
	if strings.TrimSpace(a.ID) == "" {
		a.ID = deterministicAssertionID(a)
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	return a, nil
}

// FormatAssertionExpression renders a concise, deterministic human-readable
// rule expression shown in the assertion drawer preview & list.
func FormatAssertionExpression(a domain.EvoSOPAssertion) string {
	target := "output"
	if strings.TrimSpace(a.Path) != "" {
		target = fmt.Sprintf(`artifact(%q)`, strings.TrimSpace(a.Path))
	}
	scopePrefix := ""
	if strings.TrimSpace(a.SampleRunID) != "" {
		scopePrefix = fmt.Sprintf(`sample(%q).`, strings.TrimSpace(a.SampleRunID))
	} else if strings.TrimSpace(a.TargetScope) != "" && strings.TrimSpace(a.TargetScope) != "sop" {
		scopePrefix = strings.TrimSpace(a.TargetScope) + "."
	}
	switch a.Kind {
	case domain.EvoAssertArtifactExists:
		return fmt.Sprintf(`%sartifact.exists(%q)`, scopePrefix, a.Path)
	case domain.EvoAssertJSONValid:
		if len(a.RequiredFields) > 0 {
			return fmt.Sprintf(`%s%s.json().required([%s])`, scopePrefix, target, strings.Join(a.RequiredFields, ", "))
		}
		return fmt.Sprintf(`%s%s.isValidJSON()`, scopePrefix, target)
	case domain.EvoAssertJSONField:
		if a.ExpectedValue != "" {
			return fmt.Sprintf(`%s%s.%s == %q`, scopePrefix, target, a.FieldPath, a.ExpectedValue)
		}
		if len(a.AllowedValues) > 0 {
			return fmt.Sprintf(`%s%s.%s in [%s]`, scopePrefix, target, a.FieldPath, strings.Join(a.AllowedValues, ", "))
		}
		return fmt.Sprintf(`%s%s.%s =~ /%s/`, scopePrefix, target, a.FieldPath, a.Regex)
	case domain.EvoAssertOutputRegex:
		if len(a.Contains) > 0 && a.Regex == "" {
			return fmt.Sprintf(`%s%s.contains([%s])`, scopePrefix, target, strings.Join(a.Contains, ", "))
		}
		if len(a.Contains) > 0 {
			return fmt.Sprintf(`%s%s.contains([%s]) && =~ /%s/`, scopePrefix, target, strings.Join(a.Contains, ", "), a.Regex)
		}
		return fmt.Sprintf(`%s%s =~ /%s/`, scopePrefix, target, a.Regex)
	case domain.EvoAssertGateVerdict:
		return fmt.Sprintf(`%sgate.verdict == %q`, scopePrefix, firstNonEmptyString(a.ExpectedValue, "pass"))
	case domain.EvoAssertCommandExit:
		return fmt.Sprintf(`%scommand.exit_code == %s`, scopePrefix, firstNonEmptyString(a.ExpectedValue, "0"))
	default:
		return a.Kind
	}
}

func defaultAssertionName(a domain.EvoSOPAssertion) string {
	switch a.Kind {
	case domain.EvoAssertArtifactExists:
		return "必须产出 " + a.Path
	case domain.EvoAssertJSONValid:
		if len(a.RequiredFields) > 0 {
			return fmt.Sprintf("%s 需为合法 JSON 且含字段 %s", firstNonEmptyString(a.Path, "输出"), strings.Join(a.RequiredFields, "/"))
		}
		return fmt.Sprintf("%s 需为合法 JSON", firstNonEmptyString(a.Path, "输出"))
	case domain.EvoAssertJSONField:
		if a.ExpectedValue != "" {
			return fmt.Sprintf("字段 %s 必须等于 %s", a.FieldPath, a.ExpectedValue)
		}
		if len(a.AllowedValues) > 0 {
			return fmt.Sprintf("字段 %s 只能是 %s", a.FieldPath, strings.Join(a.AllowedValues, " / "))
		}
		return fmt.Sprintf("字段 %s 需匹配正则 %s", a.FieldPath, a.Regex)
	case domain.EvoAssertOutputRegex:
		if len(a.Contains) > 0 {
			return fmt.Sprintf("%s 必须包含 %s", firstNonEmptyString(a.Path, "输出"), strings.Join(a.Contains, "、"))
		}
		return fmt.Sprintf("%s 需匹配模式 %s", firstNonEmptyString(a.Path, "输出"), a.Regex)
	case domain.EvoAssertGateVerdict:
		return "所有门禁结论必须通过"
	case domain.EvoAssertCommandExit:
		return "命令/脚本验收退出码必须为 " + firstNonEmptyString(a.ExpectedValue, "0")
	default:
		return a.Kind
	}
}

// EvaluateRunAssertions evaluates all enabled assertions against a live run.
func EvaluateRunAssertions(run domain.Run, assertions []domain.EvoSOPAssertion) domain.EvoRunAssertionReport {
	return EvaluateRunAssertionsForSource(run, run.ID, assertions)
}

// EvaluateRunAssertionsForSource evaluates all enabled assertions against a run
// (with sourceRunID used to match sample-scoped golden expectations during replay).
func EvaluateRunAssertionsForSource(run domain.Run, sourceRunID string, assertions []domain.EvoSOPAssertion) domain.EvoRunAssertionReport {
	active := ActiveSOPAssertions(assertions)
	if len(active) == 0 {
		return domain.EvoRunAssertionReport{Configured: false, AllPassed: true}
	}

	report := domain.EvoRunAssertionReport{
		Configured: true,
		AllPassed:  true,
		Results:    make([]domain.EvoAssertionResult, 0, len(active)),
	}
	for _, a := range active {
		// Golden sample expectations apply only to the pinned sample / its replay.
		if a.SampleRunID != "" && a.SampleRunID != run.ID && a.SampleRunID != sourceRunID {
			continue
		}
		passed, detail := evalOneAssertion(run, a)
		report.Total++
		if passed {
			report.Passed++
		} else {
			report.AllPassed = false
		}
		report.Results = append(report.Results, domain.EvoAssertionResult{
			AssertionID: a.ID,
			Name:        a.Name,
			Kind:        a.Kind,
			Passed:      passed,
			Detail:      detail,
		})
	}
	return report
}

func evalOneAssertion(run domain.Run, a domain.EvoSOPAssertion) (bool, string) {
	if run.Status == "failed" || run.Status == "cancelled" {
		return false, "运行终态失败或取消"
	}
	switch a.Kind {
	case domain.EvoAssertArtifactExists:
		if art, ok := findRunArtifact(run, a.Path); ok {
			return true, fmt.Sprintf("已产出 %s", firstNonEmptyString(art.Path, art.Name))
		}
		return false, fmt.Sprintf("未找到预期产物 %s", a.Path)

	case domain.EvoAssertJSONValid:
		raw, src, ok := loadAssertionText(run, a.Path)
		if !ok {
			return false, fmt.Sprintf("未找到可校验内容 %s", a.Path)
		}
		val, err := parseJSONPayload(raw)
		if err != nil {
			return false, fmt.Sprintf("%s 不是合法 JSON：%v", src, err)
		}
		if len(a.RequiredFields) > 0 {
			for _, field := range a.RequiredFields {
				if _, exists := lookupJSONPath(val, field); !exists {
					return false, fmt.Sprintf("%s 缺少必填字段 %s", src, field)
				}
			}
		}
		return true, fmt.Sprintf("%s JSON 合法且必填字段齐全", src)

	case domain.EvoAssertJSONField:
		raw, src, ok := loadAssertionText(run, a.Path)
		if !ok {
			return false, fmt.Sprintf("未找到目标内容 %s", a.Path)
		}
		val, err := parseJSONPayload(raw)
		if err != nil {
			return false, fmt.Sprintf("%s JSON 解析失败", src)
		}
		got, exists := lookupJSONPath(val, a.FieldPath)
		if !exists {
			return false, fmt.Sprintf("%s 缺少字段 %s", src, a.FieldPath)
		}
		gotStr := stringifyJSONScalar(got)
		if a.ExpectedValue != "" && !strings.EqualFold(gotStr, a.ExpectedValue) {
			return false, fmt.Sprintf("%s.%s 实际为 %q，期望 %q", src, a.FieldPath, gotStr, a.ExpectedValue)
		}
		if len(a.AllowedValues) > 0 {
			matched := false
			for _, allowed := range a.AllowedValues {
				if strings.EqualFold(gotStr, allowed) {
					matched = true
					break
				}
			}
			if !matched {
				return false, fmt.Sprintf("%s.%s 实际为 %q，不在允许范围 [%s]", src, a.FieldPath, gotStr, strings.Join(a.AllowedValues, ", "))
			}
		}
		if a.Regex != "" {
			re, err := regexp.Compile(a.Regex)
			if err != nil || !re.MatchString(gotStr) {
				return false, fmt.Sprintf("%s.%s=%q 不匹配正则 /%s/", src, a.FieldPath, gotStr, a.Regex)
			}
		}
		return true, fmt.Sprintf("%s.%s=%q 符合预期", src, a.FieldPath, gotStr)

	case domain.EvoAssertOutputRegex:
		raw, src, ok := loadAssertionText(run, a.Path)
		if !ok {
			return false, fmt.Sprintf("未找到可校验内容 %s", a.Path)
		}
		for _, kw := range a.Contains {
			if !strings.Contains(raw, kw) {
				return false, fmt.Sprintf("%s 未包含预期关键字「%s」", src, kw)
			}
		}
		if a.Regex != "" {
			re, err := regexp.Compile(a.Regex)
			if err != nil || !re.MatchString(raw) {
				return false, fmt.Sprintf("%s 未匹配正则 /%s/", src, a.Regex)
			}
		}
		return true, fmt.Sprintf("%s 内容校验通过", src)

	case domain.EvoAssertGateVerdict:
		want := strings.ToLower(firstNonEmptyString(a.ExpectedValue, "pass"))
		for _, id := range sortedRunNodeIDs(run) {
			nr := run.Nodes[id]
			if nr == nil || nr.GateResult == nil {
				continue
			}
			dec := strings.ToLower(strings.TrimSpace(nr.GateResult.Decision))
			if dec != "" && dec != want {
				return false, fmt.Sprintf("节点「%s」门禁结论为 %s（期望 %s）", nodeDisplayName(run, id), dec, want)
			}
		}
		return true, "所有门禁节点均通过"

	case domain.EvoAssertCommandExit:
		for _, id := range sortedRunNodeIDs(run) {
			nr := run.Nodes[id]
			if nr == nil {
				continue
			}
			if nr.Status == "failed" {
				return false, fmt.Sprintf("节点「%s」执行失败：%s", nodeDisplayName(run, id), domain.TrimEvoText(nr.Error, 80))
			}
			if nr.GateResult != nil && strings.ToLower(strings.TrimSpace(nr.GateResult.Decision)) == "fail" {
				return false, fmt.Sprintf("节点「%s」命令门禁退出非 0：%s", nodeDisplayName(run, id), domain.TrimEvoText(nr.GateResult.Reason, 80))
			}
		}
		return true, "命令/门禁退出码均为 0"
	}
	return false, "未知断言类型"
}

// BacktestAssertion evaluates a candidate assertion against up to 5 recent clean
// historical runs so users immediately see whether a generated rule is safe.
func BacktestAssertion(a domain.EvoSOPAssertion, runs []domain.Run) domain.EvoAssertionBacktest {
	a.Enabled = true
	if norm, err := ValidateSOPAssertion(a); err == nil {
		a = norm
	}
	bt := domain.EvoAssertionBacktest{
		Assertion:  a,
		Expression: FormatAssertionExpression(a),
		AllPassed:  true,
	}
	clean := make([]domain.Run, 0, 5)
	for _, r := range runs {
		if r.ExecutionContext.IsEvolutionReplay() || RunScore(r) != 2 {
			continue
		}
		if a.SampleRunID != "" && r.ID != a.SampleRunID {
			continue
		}
		clean = append(clean, r)
		if len(clean) >= 5 {
			break
		}
	}
	if len(clean) == 0 {
		bt.AllPassed = true
		return bt
	}
	for _, r := range clean {
		bt.TestedRuns++
		ok, detail := evalOneAssertion(r, a)
		if ok {
			bt.PassedRuns++
		} else {
			bt.AllPassed = false
			if len(bt.Details) < 3 {
				bt.Details = append(bt.Details, fmt.Sprintf("%s: %s", shortRunTag(r.ID), detail))
			}
		}
	}
	return bt
}

// SuggestAssertionsFromRuns extracts conservative, high-confidence assertions
// from the intersection of up to 5 recent clean runs (only artifacts & JSON
// keys present in EVERY sampled clean run are suggested).
func SuggestAssertionsFromRuns(runs []domain.Run) []domain.EvoSOPAssertion {
	clean := make([]domain.Run, 0, 5)
	for _, r := range runs {
		if r.ExecutionContext.IsEvolutionReplay() || RunScore(r) != 2 {
			continue
		}
		clean = append(clean, r)
		if len(clean) >= 5 {
			break
		}
	}
	if len(clean) == 0 {
		return nil
	}

	// Intersection of artifact names present across ALL clean runs.
	var commonNames []string
	firstSet := map[string]bool{}
	for _, art := range clean[0].Artifacts {
		name := canonicalArtifactName(art)
		if name != "" && !firstSet[name] {
			firstSet[name] = true
			commonNames = append(commonNames, name)
		}
	}
	sort.Strings(commonNames)

	var out []domain.EvoSOPAssertion
	for _, name := range commonNames {
		allHave := true
		var jsonKeySets []map[string]bool
		isJSON := strings.HasSuffix(strings.ToLower(name), ".json")
		for _, r := range clean {
			art, ok := findRunArtifact(r, name)
			if !ok {
				allHave = false
				break
			}
			if isJSON {
				if val, err := parseJSONPayload(art.Content); err == nil {
					if obj, ok := val.(map[string]any); ok {
						ks := map[string]bool{}
						for k := range obj {
							ks[k] = true
						}
						jsonKeySets = append(jsonKeySets, ks)
					}
				}
			}
		}
		if !allHave {
			continue
		}
		if isJSON && len(jsonKeySets) == len(clean) {
			commonKeys := intersectKeySets(jsonKeySets)
			if len(commonKeys) > 6 {
				commonKeys = commonKeys[:6]
			}
			a, err := ValidateSOPAssertion(domain.EvoSOPAssertion{
				Kind:           domain.EvoAssertJSONValid,
				Enabled:        true,
				Path:           name,
				RequiredFields: commonKeys,
				Name:           fmt.Sprintf("必须产出合法 %s", name),
			})
			if err == nil {
				out = append(out, a)
			}
		} else {
			a, err := ValidateSOPAssertion(domain.EvoSOPAssertion{
				Kind:    domain.EvoAssertArtifactExists,
				Enabled: true,
				Path:    name,
				Name:    fmt.Sprintf("必须产出 %s", name),
			})
			if err == nil {
				out = append(out, a)
			}
		}
		if len(out) >= 3 {
			break
		}
	}

	// Check whether runs have verification gates; if so, suggest gate_verdict.
	if len(out) < 3 {
		hasGate := false
		for _, r := range clean {
			for _, nr := range r.Nodes {
				if nr != nil && nr.GateResult != nil {
					hasGate = true
					break
				}
			}
			if hasGate {
				break
			}
		}
		if hasGate {
			if a, err := ValidateSOPAssertion(domain.EvoSOPAssertion{
				Kind:          domain.EvoAssertGateVerdict,
				Enabled:       true,
				ExpectedValue: "pass",
				Name:          "所有门禁验收必须通过",
			}); err == nil {
				out = append(out, a)
			}
		}
	}
	return out
}

var (
	reFileNameToken  = regexp.MustCompile(`(?i)([a-zA-Z0-9_./-]+\.(?:json|md|txt|ya?ml|csv|xml|html|log))`)
	reRunIDToken     = regexp.MustCompile(`\b(run-[a-zA-Z0-9_-]{3,})\b`)
	reFieldEquals    = regexp.MustCompile(`(?i)([a-zA-Z_][a-zA-Z0-9_.]*)\s*(?:==|=|必须等于|等于|必须为|必须是|为)\s*["“”']?([a-zA-Z0-9_.-]+)["“”']?`)
	reFieldAllowed   = regexp.MustCompile(`(?i)([a-zA-Z_][a-zA-Z0-9_.]*)\s*(?:只能是|取值|属于|必须是以下之一|in)\s*[:：]?\s*([^\n。；;]+)`)
	reRequiredFields = regexp.MustCompile(`(?i)(?:包含|必填|必须有|含字段|字段)\s*[:：]?\s*([a-zA-Z0-9_.,、/，\s]+)`)
	reContainsQuoted = regexp.MustCompile(`[「“"']([^」”"']{1,40})[」”"']`)
)

// ParseAssertionFromSentence deterministically compiles a single natural-language
// Chinese or English sentence into one or more validated Layer 2 assertions.
// Used both as a fast local parser and as fallback when LLM generation is unavailable.
func ParseAssertionFromSentence(sentence string, sampleRuns []domain.Run) []domain.EvoSOPAssertion {
	text := strings.TrimSpace(sentence)
	if text == "" {
		return nil
	}
	var out []domain.EvoSOPAssertion

	filePath := ""
	if m := reFileNameToken.FindStringSubmatch(text); len(m) >= 2 {
		filePath = strings.TrimSpace(m[1])
	}
	sampleRunID := ""
	if m := reRunIDToken.FindStringSubmatch(text); len(m) >= 2 {
		sampleRunID = strings.TrimSpace(m[1])
	}

	lower := strings.ToLower(text)

	// 1. Command exit code / gate verdict intent.
	if strings.Contains(text, "退出码") || strings.Contains(lower, "exit code") || strings.Contains(lower, "exit_code") {
		if a, err := ValidateSOPAssertion(domain.EvoSOPAssertion{
			Kind:          domain.EvoAssertCommandExit,
			Enabled:       true,
			ExpectedValue: "0",
			Prompt:        text,
		}); err == nil {
			out = append(out, a)
		}
	} else if strings.Contains(text, "门禁") && (strings.Contains(text, "通过") || strings.Contains(lower, "pass")) {
		if a, err := ValidateSOPAssertion(domain.EvoSOPAssertion{
			Kind:          domain.EvoAssertGateVerdict,
			Enabled:       true,
			ExpectedValue: "pass",
			Prompt:        text,
		}); err == nil {
			out = append(out, a)
		}
	}

	// 2. Enum / allowed values: "route 只能是 logistics / refund / human"
	if m := reFieldAllowed.FindStringSubmatch(text); len(m) >= 3 {
		field := strings.TrimSpace(m[1])
		rawVals := m[2]
		vals := splitEnumTokens(rawVals)
		if field != "" && len(vals) >= 2 {
			targetPath := filePath
			if targetPath == "" {
				targetPath = inferDefaultJSONArtifact(sampleRuns)
			}
			if a, err := ValidateSOPAssertion(domain.EvoSOPAssertion{
				Kind:          domain.EvoAssertJSONField,
				Enabled:       true,
				Path:          targetPath,
				FieldPath:     field,
				AllowedValues: vals,
				SampleRunID:   sampleRunID,
				Prompt:        text,
			}); err == nil {
				out = append(out, a)
			}
		}
	}

	// 3. Exact JSON field value: "route = logistics" / "route 必须为 logistics"
	if len(out) == 0 {
		if m := reFieldEquals.FindStringSubmatch(text); len(m) >= 3 {
			field := strings.TrimSpace(m[1])
			val := strings.TrimSpace(m[2])
			if field != "" && val != "" && !strings.EqualFold(field, "exit_code") {
				targetPath := filePath
				if targetPath == "" {
					targetPath = inferDefaultJSONArtifact(sampleRuns)
				}
				if a, err := ValidateSOPAssertion(domain.EvoSOPAssertion{
					Kind:          domain.EvoAssertJSONField,
					Enabled:       true,
					Path:          targetPath,
					FieldPath:     field,
					ExpectedValue: val,
					SampleRunID:   sampleRunID,
					Prompt:        text,
				}); err == nil {
					out = append(out, a)
				}
			}
		}
	}

	// 4. JSON file with required fields OR artifact existence.
	if filePath != "" && strings.HasSuffix(strings.ToLower(filePath), ".json") {
		var reqFields []string
		if m := reRequiredFields.FindStringSubmatch(text); len(m) >= 2 {
			reqFields = splitFieldTokens(m[1])
		}
		if len(reqFields) > 0 || strings.Contains(lower, "json") || len(out) == 0 {
			if a, err := ValidateSOPAssertion(domain.EvoSOPAssertion{
				Kind:           domain.EvoAssertJSONValid,
				Enabled:        true,
				Path:           filePath,
				RequiredFields: reqFields,
				Prompt:         text,
			}); err == nil {
				out = append(out, a)
			}
		}
	} else if filePath != "" && len(out) == 0 {
		if a, err := ValidateSOPAssertion(domain.EvoSOPAssertion{
			Kind:    domain.EvoAssertArtifactExists,
			Enabled: true,
			Path:    filePath,
			Prompt:  text,
		}); err == nil {
			out = append(out, a)
		}
	}

	// 5. Keyword / heading containment fallback (e.g. "报告必须包含「风险」和「结论」").
	if len(out) == 0 {
		var kws []string
		for _, m := range reContainsQuoted.FindAllStringSubmatch(text, 6) {
			if len(m) >= 2 && strings.TrimSpace(m[1]) != "" {
				kws = append(kws, strings.TrimSpace(m[1]))
			}
		}
		if len(kws) == 0 {
			cleaned := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(text, "必须包含"), "包含"))
			if cleaned != "" {
				kws = []string{cleaned}
			}
		}
		if len(kws) > 0 {
			if a, err := ValidateSOPAssertion(domain.EvoSOPAssertion{
				Kind:     domain.EvoAssertOutputRegex,
				Enabled:  true,
				Path:     filePath,
				Contains: kws,
				Prompt:   text,
			}); err == nil {
				out = append(out, a)
			}
		}
	}

	return dedupeAssertions(out)
}

// BuildAssertionGenPrompt builds a concise LLM prompt to translate one user
// sentence into structured deterministic EvoSOPAssertion JSON items.
func BuildAssertionGenPrompt(wf domain.WorkflowDefinition, sentence string, sampleRuns []domain.Run) (string, string) {
	system := `你是 LoopForge 结果验收规则编译器。请把用户的一句话验收要求转换为 1 到 3 条确定性规则 JSON（禁止输出多余解释）。
支持的 kind：
- artifact_exists: {"kind":"artifact_exists","name":"...","path":"文件名"}
- json_valid: {"kind":"json_valid","name":"...","path":"文件名(可空)","requiredFields":["field1","field2"]}
- json_field: {"kind":"json_field","name":"...","path":"文件名(可空)","fieldPath":"route","allowedValues":["a","b"],"expectedValue":"","regex":""}
- output_regex: {"kind":"output_regex","name":"...","path":"文件名(可空)","contains":["关键字"],"regex":""}
- gate_verdict: {"kind":"gate_verdict","name":"...","expectedValue":"pass"}
- command_exit: {"kind":"command_exit","name":"...","expectedValue":"0"}
输出格式严格为 JSON 对象：{"assertions":[...]}`

	var sb strings.Builder
	sb.WriteString("SOP 名称：")
	sb.WriteString(firstNonEmptyString(wf.Name, wf.ID))
	sb.WriteString("\n")
	if len(sampleRuns) > 0 {
		names := map[string]bool{}
		for _, r := range sampleRuns {
			for _, art := range r.Artifacts {
				if n := canonicalArtifactName(art); n != "" {
					names[n] = true
				}
			}
		}
		if len(names) > 0 {
			list := make([]string, 0, len(names))
			for n := range names {
				list = append(list, n)
			}
			sort.Strings(list)
			sb.WriteString("最近历史运行产物：")
			sb.WriteString(strings.Join(list, ", "))
			sb.WriteString("\n")
		}
	}
	sb.WriteString("用户一句话验收要求：\n")
	sb.WriteString(strings.TrimSpace(sentence))
	return system, sb.String()
}

// ParseAssertionGenResponse extracts validated assertions from an LLM JSON reply.
func ParseAssertionGenResponse(raw, originalPrompt string) []domain.EvoSOPAssertion {
	body := extractJSONObject(raw)
	if body == "" {
		return nil
	}
	var payload struct {
		Assertions []domain.EvoSOPAssertion `json:"assertions"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return nil
	}
	var out []domain.EvoSOPAssertion
	for _, a := range payload.Assertions {
		a.Enabled = true
		if strings.TrimSpace(a.Prompt) == "" {
			a.Prompt = strings.TrimSpace(originalPrompt)
		}
		if norm, err := ValidateSOPAssertion(a); err == nil {
			out = append(out, norm)
		}
		if len(out) >= 4 {
			break
		}
	}
	return dedupeAssertions(out)
}

func findRunArtifact(run domain.Run, want string) (domain.Artifact, bool) {
	want = strings.TrimSpace(want)
	if want == "" {
		return domain.Artifact{}, false
	}
	wantLower := strings.ToLower(want)
	wantBase := strings.ToLower(path.Base(want))
	// Prefer latest attempt if multiple snapshots exist.
	bestIdx := -1
	bestAttempt := -1
	for i, art := range run.Artifacts {
		nLower := strings.ToLower(strings.TrimSpace(art.Name))
		pLower := strings.ToLower(strings.TrimSpace(art.Path))
		matched := nLower == wantLower || pLower == wantLower ||
			strings.HasSuffix(pLower, "/"+wantLower) ||
			(wantBase != "" && (path.Base(pLower) == wantBase || path.Base(nLower) == wantBase))
		if matched && art.Attempt >= bestAttempt {
			bestIdx = i
			bestAttempt = art.Attempt
		}
	}
	if bestIdx >= 0 {
		return run.Artifacts[bestIdx], true
	}
	return domain.Artifact{}, false
}

func loadAssertionText(run domain.Run, filePath string) (string, string, bool) {
	filePath = strings.TrimSpace(filePath)
	if filePath != "" {
		if art, ok := findRunArtifact(run, filePath); ok {
			return art.Content, firstNonEmptyString(art.Path, art.Name, filePath), true
		}
		return "", filePath, false
	}
	// If no file specified, prefer latest JSON/text artifact if any, else node outputs.
	var sb strings.Builder
	for _, id := range sortedRunNodeIDs(run) {
		nr := run.Nodes[id]
		if nr != nil && strings.TrimSpace(nr.Output) != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(nr.Output)
		}
	}
	if sb.Len() > 0 {
		return sb.String(), "节点输出", true
	}
	if len(run.Artifacts) > 0 {
		last := run.Artifacts[len(run.Artifacts)-1]
		return last.Content, firstNonEmptyString(last.Path, last.Name), true
	}
	return "", "运行输出", false
}

func parseJSONPayload(raw string) (any, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("空内容")
	}
	var val any
	if err := json.Unmarshal([]byte(trimmed), &val); err == nil {
		return val, nil
	}
	if extracted := extractJSONObject(trimmed); extracted != "" {
		if err := json.Unmarshal([]byte(extracted), &val); err == nil {
			return val, nil
		}
	}
	return nil, errors.New("JSON 语法无效")
}

func lookupJSONPath(root any, fieldPath string) (any, bool) {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(fieldPath), "$."), ".")
	cur := root
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		next, exists := obj[p]
		if !exists {
			return nil, false
		}
		cur = next
	}
	if cur == nil {
		return nil, false
	}
	if s, ok := cur.(string); ok && strings.TrimSpace(s) == "" {
		return nil, false
	}
	return cur, true
}

func stringifyJSONScalar(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%v", x)
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func canonicalArtifactName(art domain.Artifact) string {
	if p := strings.TrimSpace(art.Path); p != "" {
		return path.Base(p)
	}
	return strings.TrimSpace(art.Name)
}

func intersectKeySets(sets []map[string]bool) []string {
	if len(sets) == 0 {
		return nil
	}
	var out []string
	for k := range sets[0] {
		ok := true
		for i := 1; i < len(sets); i++ {
			if !sets[i][k] {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func inferDefaultJSONArtifact(runs []domain.Run) string {
	for _, r := range runs {
		for _, art := range r.Artifacts {
			name := canonicalArtifactName(art)
			if strings.HasSuffix(strings.ToLower(name), ".json") {
				return name
			}
		}
	}
	return ""
}

func splitEnumTokens(raw string) []string {
	r := strings.NewReplacer("、", ",", "，", ",", "/", ",", "|", ",", "或", ",", "和", ",", "[", "", "]", "", "（", "", "）", "", "“", "", "”", "", `"`, "", "'", "")
	parts := strings.Split(r.Replace(raw), ",")
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, p := range parts {
		tok := strings.TrimSpace(p)
		if tok == "" || len(tok) > 40 || seen[tok] {
			continue
		}
		seen[tok] = true
		out = append(out, tok)
	}
	return out
}

func splitFieldTokens(raw string) []string {
	r := strings.NewReplacer("、", ",", "，", ",", "/", ",", "和", ",", "与", ",", "及", ",", "字段", "")
	parts := strings.Split(r.Replace(raw), ",")
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, p := range parts {
		for _, sub := range strings.Fields(p) {
			tok := strings.Trim(sub, ".,;:`\"'()[]{}")
			if tok == "" || len(tok) > 48 || seen[tok] {
				continue
			}
			// Keep identifier-like field names only.
			valid := true
			for _, ch := range tok {
				if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '-' || ch == '.') {
					valid = false
					break
				}
			}
			if !valid || strings.HasSuffix(strings.ToLower(tok), ".json") {
				continue
			}
			seen[tok] = true
			out = append(out, tok)
		}
	}
	return out
}

func cleanStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, v := range in {
		s := strings.TrimSpace(v)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func deterministicAssertionID(a domain.EvoSOPAssertion) string {
	sum := sha256.Sum256([]byte(a.Kind + "|" + a.Expression))
	return "eva-" + hex.EncodeToString(sum[:])[:10]
}

func dedupeAssertions(in []domain.EvoSOPAssertion) []domain.EvoSOPAssertion {
	seen := map[string]bool{}
	out := make([]domain.EvoSOPAssertion, 0, len(in))
	for _, a := range in {
		key := a.Kind + "|" + a.Expression
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, a)
	}
	return out
}

func shortRunTag(id string) string {
	if len(id) > 12 {
		return id[len(id)-12:]
	}
	return id
}
