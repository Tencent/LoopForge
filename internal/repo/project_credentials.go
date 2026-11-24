package repo

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"
	"gorm.io/gorm"
)

var (
	ErrProjectCredentialUnavailable = errors.New("项目凭据不存在、已停用或不属于当前项目")
	ErrProjectCredentialKey         = errors.New("请为平台配置 AGENTFLOW_PROJECT_CREDENTIAL_KEY（Base64 编码的 32 字节密钥）")
	ErrProjectCredentialName        = errors.New("该项目已有同名凭据")
)

type ProjectCredentialsRepo struct {
	*deps
	projects *ProjectsRepo
}

type projectCredentialRow struct {
	ID         string    `gorm:"column:id;primaryKey;size:64"`
	ProjectID  string    `gorm:"column:project_id;size:64;not null;uniqueIndex:idx_project_credential_name,priority:1"`
	Name       string    `gorm:"column:name;size:80;not null;uniqueIndex:idx_project_credential_name,priority:2"`
	Kind       string    `gorm:"column:kind;size:32;not null"`
	Variables  []byte    `gorm:"column:variables;type:json;not null"`
	Ciphertext []byte    `gorm:"column:ciphertext;type:mediumblob;not null"`
	Disabled   bool      `gorm:"column:disabled;not null;default:false"`
	UpdatedAt  time.Time `gorm:"column:updated_at;not null"`
}

func (projectCredentialRow) TableName() string { return "project_credentials" }

func (row projectCredentialRow) metadata() (domain.ProjectCredential, error) {
	out := domain.ProjectCredential{ID: row.ID, ProjectID: row.ProjectID, Name: row.Name, Kind: row.Kind, Disabled: row.Disabled, UpdatedAt: row.UpdatedAt}
	if err := json.Unmarshal(row.Variables, &out.Variables); err != nil {
		return domain.ProjectCredential{}, fmt.Errorf("读取项目凭据字段失败")
	}
	return out, nil
}

func (r *ProjectCredentialsRepo) ProjectCredentials(ctx context.Context, projectID string) ([]domain.ProjectCredential, error) {
	var rows []projectCredentialRow
	err := r.DB.WithContext(ctx).Omit("ciphertext").Where("project_id = ?", projectID).Order("name, id").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]domain.ProjectCredential, 0, len(rows))
	for _, row := range rows {
		if row.ProjectID != projectID {
			continue
		}
		item, err := row.metadata()
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

func projectCredentialCipher() (cipher.AEAD, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("AGENTFLOW_PROJECT_CREDENTIAL_KEY")))
	if err != nil || len(key) != 32 {
		return nil, ErrProjectCredentialKey
	}
	block, err := aes.NewCipher(key)
	clear(key)
	if err != nil {
		return nil, ErrProjectCredentialKey
	}
	return cipher.NewGCMWithRandomNonce(block)
}

func (row projectCredentialRow) associatedData() []byte {
	return []byte("loopforge/project-credential/v1\x00" + row.ProjectID + "\x00" + row.ID)
}

func (row projectCredentialRow) open(box cipher.AEAD) (map[string]string, error) {
	data, err := box.Open(nil, nil, row.Ciphertext, row.associatedData())
	if err != nil {
		return nil, fmt.Errorf("无法解密项目凭据，请检查平台密钥")
	}
	defer clear(data)
	var values map[string]string
	if err := json.Unmarshal(data, &values); err != nil || len(values) == 0 {
		return nil, fmt.Errorf("项目凭据数据无效")
	}
	return values, nil
}

func (r *ProjectCredentialsRepo) SaveProjectCredential(ctx context.Context, projectID, id string, create bool, input domain.ProjectCredentialInput) (domain.ProjectCredential, error) {
	if err := input.Validate(); err != nil {
		return domain.ProjectCredential{}, err
	}
	box, err := projectCredentialCipher()
	if err != nil {
		return domain.ProjectCredential{}, err
	}
	row := projectCredentialRow{ID: id, ProjectID: projectID}
	err = r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var previous map[string]string
		if !create {
			if err := r.locking(tx).Where("project_id = ? AND id = ?", projectID, id).Take(&row).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrProjectCredentialUnavailable
				}
				return err
			}
			if row.ProjectID != projectID || row.ID != id {
				return ErrProjectCredentialUnavailable
			}
			// Replacing every value also lets the owner recover after a key change.
			if slices.ContainsFunc(input.Fields, func(field domain.ProjectCredentialVariableInput) bool { return field.Value == nil }) {
				var err error
				previous, err = row.open(box)
				if err != nil {
					return err
				}
			}
		}
		values := make(map[string]string, len(input.Fields))
		names := make([]string, 0, len(input.Fields))
		size := 0
		for _, field := range input.Fields {
			oldName := field.Name
			if field.PreviousName != "" {
				oldName = field.PreviousName
			}
			value := previous[oldName]
			if field.Value != nil {
				value = *field.Value
			}
			if value == "" || strings.ContainsRune(value, 0) {
				return fmt.Errorf("请填写有效的 %s 值", field.Name)
			}
			size += len(field.Name) + len(value) + 2
			if size > domain.ProjectCredentialEnvLimit {
				return fmt.Errorf("凭据变量合计不能超过 64 KiB")
			}
			names = append(names, field.Name)
			values[field.Name] = value
		}
		row.Name, row.Kind, row.Disabled = strings.TrimSpace(input.Name), input.Kind, input.Disabled
		row.UpdatedAt = time.Now().UTC()
		row.Variables = mustJSON(names)
		plaintext := mustJSON(values)
		row.Ciphertext = box.Seal(nil, nil, plaintext, row.associatedData())
		clear(plaintext)
		if create {
			return tx.Create(&row).Error
		}
		return tx.Model(&row).Where("project_id = ? AND id = ?", projectID, id).
			Select("name", "kind", "variables", "ciphertext", "disabled", "updated_at").Updates(&row).Error
	})
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		err = ErrProjectCredentialName
	}
	if err != nil {
		return domain.ProjectCredential{}, err
	}
	return row.metadata()
}

func (r *ProjectCredentialsRepo) DeleteProjectCredential(ctx context.Context, projectID, id string) error {
	result := r.DB.WithContext(ctx).Where("project_id = ? AND id = ?", projectID, id).Delete(&projectCredentialRow{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrProjectCredentialUnavailable
	}
	return nil
}

func (r *ProjectCredentialsRepo) selectedProjectCredentials(ctx context.Context, projectID string, ids []string, includeValues bool) ([]projectCredentialRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > 16 {
		return nil, fmt.Errorf("一次最多绑定 16 条项目凭据")
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			return nil, fmt.Errorf("绑定的项目凭据重复")
		}
		seen[id] = true
	}
	project, ok := r.projects.Project(projectID)
	if !ok || project.ID != projectID || project.ArchivedAt != nil {
		return nil, ErrProjectCredentialUnavailable
	}
	query := r.DB.WithContext(ctx).Where("project_id = ? AND id IN ? AND disabled = ?", projectID, ids, false)
	if !includeValues {
		query = query.Omit("ciphertext")
	}
	var rows []projectCredentialRow
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) != len(ids) {
		return nil, ErrProjectCredentialUnavailable
	}
	matched := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row.ProjectID != projectID {
			return nil, ErrProjectCredentialUnavailable
		}
		matched[row.ID] = true
	}
	for _, id := range ids {
		if !matched[id] {
			return nil, ErrProjectCredentialUnavailable
		}
	}
	return rows, nil
}

// ProjectCredentialVariableNames validates bindings without decrypting values.
func (r *ProjectCredentialsRepo) ProjectCredentialVariableNames(ctx context.Context, projectID string, ids []string) ([]string, error) {
	rows, err := r.selectedProjectCredentials(ctx, projectID, ids, false)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(ids))
	seen := make(map[string]bool)
	for _, row := range rows {
		item, err := row.metadata()
		if err != nil {
			return nil, err
		}
		for _, name := range item.Variables {
			if err := domain.ValidateProjectCredentialVariable(name); err != nil {
				return nil, err
			}
			key := strings.ToUpper(name)
			if seen[key] {
				return nil, fmt.Errorf("所选凭据的环境变量 %s 冲突", name)
			}
			seen[key] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// ResolveProjectCredentialEnv is called with a project derived from the saved
// workflow or authenticated conversation. Plaintext is held for execution only.
func (r *ProjectCredentialsRepo) ResolveProjectCredentialEnv(ctx context.Context, projectID string, ids []string) (map[string]string, error) {
	rows, err := r.selectedProjectCredentials(ctx, projectID, ids, true)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	box, err := projectCredentialCipher()
	if err != nil {
		return nil, err
	}
	env := make(map[string]string)
	seen := make(map[string]bool)
	size := 0
	for _, row := range rows {
		values, err := row.open(box)
		if err != nil {
			return nil, err
		}
		for name, value := range values {
			if err := domain.ValidateProjectCredentialVariable(name); err != nil {
				return nil, err
			}
			key := strings.ToUpper(name)
			if seen[key] {
				return nil, fmt.Errorf("所选凭据的环境变量 %s 冲突", name)
			}
			seen[key] = true
			env[name] = value
			size += len(name) + len(value) + 2
		}
	}
	if size > domain.ProjectCredentialEnvLimit {
		return nil, fmt.Errorf("凭据变量合计不能超过 64 KiB")
	}
	return env, nil
}
