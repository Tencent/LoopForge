package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// User 表示当前登录或降级后的员工身份。
type User struct {
	ID              int64     `json:"id"`
	StaffID         int       `json:"staffId"`
	LoginName       string    `json:"loginName"`
	ChineseName     string    `json:"chineseName"`
	DeptID          int       `json:"deptId"`
	DeptName        string    `json:"deptName"`
	IsGuest         bool      `json:"isGuest"`
	IsPlatformAdmin bool      `json:"isPlatformAdmin,omitempty"`
	AuthTime        time.Time `json:"authTime"`
	AvatarURL       string    `json:"avatarUrl,omitempty"`
}

// ToDomain 将 auth.User 转换为持久化层实体 domain.User。
func (u *User) ToDomain() domain.User {
	if u == nil {
		return domain.User{}
	}
	return domain.User{
		ID:          u.ID,
		StaffID:     u.StaffID,
		LoginName:   u.LoginName,
		ChineseName: u.ChineseName,
		DeptID:      u.DeptID,
		DeptName:    u.DeptName,
	}
}

// IOAAuthResponse 是太湖 / TOF4 / TAI AccessToken 接口的响应结构。
type IOAAuthResponse struct {
	Ret        int          `json:"Ret"`
	ErrCode    int          `json:"ErrCode"`
	ErrMsg     string       `json:"ErrMsg"`
	StackTrace string       `json:"StackTrace"`
	Data       *IOAAuthData `json:"Data"`
}

// IOAAuthData 包含了太湖发放的票据与员工核心信息。
type IOAAuthData struct {
	Key          string `json:"Key"`
	Expiration   string `json:"Expiration"`
	IsPersistent bool   `json:"IsPersistent"`
	IssueDate    string `json:"IssueDate"`
	StaffId      int    `json:"StaffId"`
	LoginName    string `json:"LoginName"`
	ChineseName  string `json:"ChineseName"`
	DeptId       int    `json:"DeptId"`
	DeptName     string `json:"DeptName"`
	Version      int    `json:"Version"`
	Token        string `json:"Token"`
	Ticket       string `json:"Ticket"`
}

// SignUser 生成带 HMAC-SHA256 签名的 User 会话字符串，便于安全地存入 Cookie。
func SignUser(u *User, secret string) (string, error) {
	if u == nil {
		return "", errors.New("user is nil")
	}
	payload, err := json.Marshal(u)
	if err != nil {
		return "", err
	}
	b64Payload := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(b64Payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("%s.%s", b64Payload, sig), nil
}

// ParseSignedUser 解析并校验被 SignUser 签名的会话字符串。
func ParseSignedUser(tokenStr, secret string) (*User, error) {
	parts := strings.Split(tokenStr, ".")
	if len(parts) != 2 {
		return nil, errors.New("invalid token format")
	}
	b64Payload, sig := parts[0], parts[1]
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(b64Payload))
	expectedSig := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expectedSig)) {
		return nil, errors.New("invalid token signature")
	}

	raw, err := base64.RawURLEncoding.DecodeString(b64Payload)
	if err != nil {
		return nil, err
	}
	var u User
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, err
	}
	return &u, nil
}
