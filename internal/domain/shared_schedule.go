package domain

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type ScheduleParameter struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
	Default  string `json:"default,omitempty"`
}

// SharedScheduleVersion is an immutable, credential-free published definition.
type SharedScheduleVersion struct {
	ID                string              `json:"id"`
	Version           int                 `json:"version"`
	SourceProjectID   string              `json:"sourceProjectId"`
	SourceProjectName string              `json:"sourceProjectName"`
	SourceJobID       string              `json:"sourceJobId"`
	Name              string              `json:"name"`
	Description       string              `json:"description"`
	Prompt            string              `json:"prompt"`
	Model             string              `json:"model"`
	Cron              string              `json:"cron"`
	IntervalSec       int                 `json:"intervalSec"`
	Parameters        []ScheduleParameter `json:"parameters"`
	ChangeNote        string              `json:"changeNote"`
	CreatedAt         time.Time           `json:"createdAt"`
}

var scheduleParameterName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,31}$`)
var scheduleParameterToken = regexp.MustCompile(`\{\{\s*params\.([^{}]+?)\s*\}\}`)

func (v SharedScheduleVersion) Validate() error {
	if strings.TrimSpace(v.Name) == "" || len(v.Name) > 255 || strings.TrimSpace(v.Prompt) == "" {
		return fmt.Errorf("共享名称和提示词必填，名称最多 255 字节")
	}
	if len(v.Description) > 2000 || len(v.ChangeNote) > 1000 || len(v.Parameters) > 20 {
		return fmt.Errorf("描述、版本说明或参数数量超出限制")
	}
	known := map[string]bool{}
	for _, p := range v.Parameters {
		if !scheduleParameterName.MatchString(p.Name) || known[p.Name] {
			return fmt.Errorf("参数名必须唯一，以字母开头，使用字母、数字或下划线：%s", p.Name)
		}
		if p.Label == "" || len(p.Label) > 120 {
			return fmt.Errorf("请填写参数 %s 的显示名称", p.Name)
		}
		if _, err := normalizeScheduleParameter(p, p.Default, false); err != nil {
			return err
		}
		known[p.Name] = true
	}
	for _, m := range scheduleParameterToken.FindAllStringSubmatch(v.Prompt, -1) {
		if !known[strings.TrimSpace(m[1])] {
			return fmt.Errorf("提示词引用了未定义参数：%s", strings.TrimSpace(m[1]))
		}
	}
	return nil
}

func normalizeScheduleParameter(p ScheduleParameter, value string, enforceRequired bool) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) > 2000 {
		return "", fmt.Errorf("参数 %s 过长", p.Name)
	}
	switch p.Type {
	case "string", "integer", "number", "boolean":
	default:
		return "", fmt.Errorf("参数 %s 类型无效", p.Name)
	}
	if value == "" {
		if enforceRequired && p.Required {
			return "", fmt.Errorf("请填写参数：%s", p.Label)
		}
		return "", nil
	}
	valid := true
	switch p.Type {
	case "integer":
		_, err := strconv.ParseInt(value, 10, 64)
		valid = err == nil
	case "number":
		n, err := strconv.ParseFloat(value, 64)
		valid = err == nil && !math.IsNaN(n) && !math.IsInf(n, 0)
	case "boolean":
		valid = value == "true" || value == "false"
	}
	if !valid {
		return "", fmt.Errorf("参数 %s 需要有效的 %s 值", p.Label, p.Type)
	}
	return value, nil
}

// Render resolves only this pinned version, in a single pass. Parameter values
// containing template syntax remain literal and cannot expand other parameters.
func (v SharedScheduleVersion) Render(input map[string]string) (string, map[string]string, error) {
	if err := v.Validate(); err != nil {
		return "", nil, err
	}
	values := map[string]string{}
	for _, p := range v.Parameters {
		value, ok := input[p.Name]
		if !ok {
			value = p.Default
		}
		normalized, err := normalizeScheduleParameter(p, value, true)
		if err != nil {
			return "", nil, err
		}
		values[p.Name] = normalized
	}
	for key := range input {
		if _, ok := values[key]; !ok {
			return "", nil, fmt.Errorf("未知参数：%s", key)
		}
	}
	prompt := scheduleParameterToken.ReplaceAllStringFunc(v.Prompt, func(token string) string {
		return values[strings.TrimSpace(scheduleParameterToken.FindStringSubmatch(token)[1])]
	})
	return prompt, values, nil
}
