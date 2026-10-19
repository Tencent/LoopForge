package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var (
	// ErrPhotoNotFound 表示未在 TOF 系统中找到该员工的头像图片。
	ErrPhotoNotFound = errors.New("photo not found")

	// ErrInvalidUsername 表示员工英文名为空或包含非法字符。
	ErrInvalidUsername = errors.New("invalid username")

	// ErrCredentialsNotConfigured 表示 PaaSID 或 AppToken 未配置。
	ErrCredentialsNotConfigured = errors.New("tai credentials not configured")
)

// NormalizePhotoSpec 将任意尺寸归一化为 TOF 头像服务支持的四种规格之一：48, 100, 150, 500。
// 缺省或 0 值默认归一化为 100。
func NormalizePhotoSpec(spec int) int {
	switch {
	case spec <= 0:
		return 100
	case spec <= 48:
		return 48
	case spec <= 100:
		if spec <= 74 {
			return 48
		}
		return 100
	case spec <= 150:
		if spec <= 125 {
			return 100
		}
		return 150
	default:
		if spec <= 300 {
			return 150
		}
		return 500
	}
}

// SanitizeUsername 校验并清理员工英文名，去除扩展名与首尾空白，防止路径穿越。
func SanitizeUsername(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", ErrInvalidUsername
	}

	// 去除可能携带的 .png / .jpg / .jpeg 扩展名
	lower := strings.ToLower(name)
	for _, ext := range []string{".png", ".jpg", ".jpeg"} {
		if strings.HasSuffix(lower, ext) {
			name = name[:len(name)-len(ext)]
			break
		}
	}
	name = strings.TrimSpace(name)
	if name == "" || strings.Contains(name, "..") || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return "", ErrInvalidUsername
	}

	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.') {
			return "", ErrInvalidUsername
		}
	}

	return name, nil
}

// AvatarURL 返回指定员工英文名与规格的系统头像相对 API 访问路径。
func AvatarURL(username string, spec int) string {
	cleanName, err := SanitizeUsername(username)
	if err != nil {
		return ""
	}
	normSpec := NormalizePhotoSpec(spec)
	return fmt.Sprintf("/api/auth/photo/%d/%s.png", normSpec, url.PathEscape(cleanName))
}

type photoCacheEntry struct {
	data        []byte
	contentType string
	expireAt    time.Time
}

type photoCache struct {
	mu      sync.RWMutex
	ttl     time.Duration
	maxSize int
	entries map[string]photoCacheEntry
}

func newPhotoCache(ttl time.Duration, maxSize int) *photoCache {
	if ttl <= 0 {
		ttl = time.Hour
	}
	if maxSize <= 0 {
		maxSize = 1000
	}
	return &photoCache{
		ttl:     ttl,
		maxSize: maxSize,
		entries: make(map[string]photoCacheEntry),
	}
}

func (c *photoCache) get(key string) ([]byte, string, bool) {
	if c == nil {
		return nil, "", false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.entries[key]
	if !ok || time.Now().After(entry.expireAt) {
		return nil, "", false
	}
	return entry.data, entry.contentType, true
}

func (c *photoCache) set(key string, data []byte, contentType string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	// 简单容量清理：如果达到上限，清理已过期的 key
	if len(c.entries) >= c.maxSize {
		now := time.Now()
		for k, v := range c.entries {
			if now.After(v.expireAt) {
				delete(c.entries, k)
			}
		}
		// 若仍然超限，清空一半
		if len(c.entries) >= c.maxSize {
			count := 0
			for k := range c.entries {
				delete(c.entries, k)
				count++
				if count >= c.maxSize/2 {
					break
				}
			}
		}
	}

	c.entries[key] = photoCacheEntry{
		data:        data,
		contentType: contentType,
		expireAt:    time.Now().Add(c.ttl),
	}
}

func (c *photoCache) clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]photoCacheEntry)
}

// ClearPhotoCache 清空内存中的头像图片缓存。
func (c *Client) ClearPhotoCache() {
	if c != nil && c.photoCache != nil {
		c.photoCache.clear()
	}
}

// FetchPhoto 请求太湖 / TOF 认证服务的员工头像接口（GET /ebus/rhrc/photo/{spec}/{username}.png）。
// 返回头像二进制字节流、HTTP Content-Type（如 image/png）及可能的错误。
func (c *Client) FetchPhoto(ctx context.Context, rawUsername string, rawSpec int) ([]byte, string, error) {
	username, err := SanitizeUsername(rawUsername)
	if err != nil {
		return nil, "", err
	}
	spec := NormalizePhotoSpec(rawSpec)

	cacheKey := fmt.Sprintf("%d:%s", spec, username)
	if data, ct, ok := c.photoCache.get(cacheKey); ok {
		return data, ct, nil
	}

	if c.cfg.PaaSID == "" || c.cfg.AppToken == "" {
		log.Printf("[auth] PaaSID or AppToken is not configured, cannot fetch photo for %s", username)
		return nil, "", ErrCredentialsNotConfigured
	}

	photoPath := c.cfg.PhotoURI
	if photoPath == "" {
		photoPath = DefaultPhotoURI
	}
	reqURL := fmt.Sprintf("%s%s/%d/%s.png", c.cfg.AuthHost, photoPath, spec, url.PathEscape(username))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("create photo request failed: %w", err)
	}

	for k, v := range c.GenerateRIOHeaders() {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "image/png,image/*;q=0.8,*/*;q=0.5")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("request TOF photo failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, "", ErrPhotoNotFound
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, "", fmt.Errorf("unexpected status %d from TOF photo: %s", resp.StatusCode, string(respBody))
	}

	// 限制头像读取大小最大 5MB
	data, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return nil, "", fmt.Errorf("read photo response body failed: %w", err)
	}
	if len(data) == 0 {
		return nil, "", ErrPhotoNotFound
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" || strings.Contains(contentType, "text/") || strings.Contains(contentType, "application/json") {
		contentType = http.DetectContentType(data)
	}
	if !strings.HasPrefix(contentType, "image/") {
		contentType = "image/png"
	}

	c.photoCache.set(cacheKey, data, contentType)
	return data, contentType, nil
}
