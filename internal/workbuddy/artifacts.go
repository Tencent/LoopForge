package workbuddy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"agentflow/internal/domain"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	api "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/workbuddyenterprise/v20260709"
)

func (c *Client) StageArtifacts(ctx context.Context, session Session, contracts []domain.ArtifactContract, directory string, warn func(string)) error {
	if len(contracts) == 0 {
		return nil
	}
	paths := make([]string, len(contracts))
	for i, contract := range contracts {
		rel := path.Clean(contract.Path)
		if rel == "." || !fs.ValidPath(rel) || strings.Contains(rel, "\\") {
			return fmt.Errorf("WorkBuddy 产物 %s 的路径无效", contract.Name)
		}
		paths[i] = rel
	}
	var files map[string]*api.SessionArtifactDetail
	for {
		listed, err := c.listArtifacts(ctx, session.ID)
		if err != nil {
			return err
		}
		files = listed
		var missing []string
		for i, contract := range contracts {
			if contract.Required && files[paths[i]] == nil {
				missing = append(missing, paths[i])
			}
		}
		if len(missing) == 0 {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("WorkBuddy 会话未登记必需产物 %s：%w", strings.Join(missing, "、"), ctx.Err())
		case <-time.After(time.Second):
		}
	}
	for i, contract := range contracts {
		rel := paths[i]
		item := files[rel]
		if item == nil {
			continue
		}
		if err := c.stageArtifact(ctx, item, rel, directory); err != nil {
			if contract.Required {
				return fmt.Errorf("WorkBuddy 产物 %s：%w", rel, err)
			}
			if warn != nil {
				warn(fmt.Sprintf("跳过可选 WorkBuddy 产物 %s：%v", rel, err))
			}
		}
	}
	return nil
}

func (c *Client) listArtifacts(ctx context.Context, sessionID string) (map[string]*api.SessionArtifactDetail, error) {
	const limit int64 = 100
	files := make(map[string]*api.SessionArtifactDetail)
	for offset := int64(0); ; offset += limit {
		request := api.NewDescribeSessionArtifactListRequest()
		request.SessionId = common.StringPtr(sessionID)
		request.Offset = common.Int64Ptr(offset)
		request.Limit = common.Int64Ptr(limit)
		result, err := c.api.DescribeSessionArtifactListWithContext(ctx, request)
		if err != nil {
			return nil, apiError("DescribeSessionArtifactList", err)
		}
		if result.Response == nil {
			return nil, fmt.Errorf("腾讯云接口未返回 WorkBuddy 产物列表")
		}
		for _, item := range result.Response.ArtifactSet {
			if item == nil || value(item.SessionId) != sessionID || value(item.Status) != "READY" || value(item.ArtifactId) == "" {
				continue
			}
			filePath := value(item.FilePath)
			if filePath == "" {
				filePath = value(item.FileName)
			}
			rel := strings.TrimPrefix(filePath, "/workspace/")
			if fs.ValidPath(rel) && path.Base(rel) == value(item.FileName) {
				if previous := files[rel]; previous == nil || value(previous.CreatedTime) < value(item.CreatedTime) {
					files[rel] = item
				}
			}
		}
		if int64(len(result.Response.ArtifactSet)) < limit || offset+limit >= value(result.Response.TotalCount) {
			return files, nil
		}
	}
}

func (c *Client) stageArtifact(ctx context.Context, item *api.SessionArtifactDetail, rel, directory string) error {
	request := api.NewDescribeSessionArtifactDownloadUrlRequest()
	request.ArtifactId = item.ArtifactId
	result, err := c.api.DescribeSessionArtifactDownloadUrlWithContext(ctx, request)
	if err != nil {
		return apiError("DescribeSessionArtifactDownloadUrl", err)
	}
	file := result.Response
	if file == nil || value(file.FileName) != path.Base(rel) || file.FileSizeBytes == nil || *file.FileSizeBytes < 0 {
		return fmt.Errorf("下载信息与产物不匹配")
	}
	if *file.FileSizeBytes > domain.MaxSOPArtifactBytes {
		return domain.ErrArtifactTooLarge
	}
	expected, err := hex.DecodeString(value(file.FileSha256))
	if err != nil || len(expected) != sha256.Size {
		return fmt.Errorf("腾讯云接口未返回有效的文件 SHA-256")
	}
	url, err := c.serviceURL(value(file.DownloadUrl), false)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url.String(), nil)
	if err != nil {
		return fmt.Errorf("无法创建 WorkBuddy 产物下载请求")
	}
	response, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("WorkBuddy 产物下载请求失败")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败（HTTP %d）", response.StatusCode)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, *file.FileSizeBytes+1))
	if err != nil {
		return fmt.Errorf("读取下载内容失败")
	}
	digest := sha256.Sum256(content)
	if int64(len(content)) != *file.FileSizeBytes || !bytes.Equal(digest[:], expected) {
		return fmt.Errorf("文件大小或 SHA-256 校验失败")
	}
	destination := filepath.Join(directory, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	return os.WriteFile(destination, content, 0600)
}
