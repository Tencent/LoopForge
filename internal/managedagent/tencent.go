package managedagent

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

const tencentService = "workbuddyenterprise"
const tencentVersion = "2026-07-09"

var tencentRegion = regexp.MustCompile("^[a-z]{2}-[a-z0-9]+$")

type TencentCloudConfig struct {
	SecretID  string
	SecretKey string
	Token     string
	Region    string
}

type tencentCloud struct {
	config TencentCloudConfig
	host   string
	client *http.Client
}

func newTencentCloud(config TencentCloudConfig, client *http.Client) (*tencentCloud, error) {
	if config.Region == "" {
		config.Region = "ap-beijing"
	}
	if config.SecretID == "" || config.SecretKey == "" || !tencentRegion.MatchString(config.Region) {
		return nil, errors.New("WMA Tencent Cloud mode requires a SecretId, SecretKey and valid region")
	}
	return &tencentCloud{config: config, host: tencentService + "." + config.Region + ".tencentcloudapi.com", client: client}, nil
}

func (c *tencentCloud) call(ctx context.Context, action string, input any, output any) error {
	ctx, stop := context.WithTimeout(ctx, 20*time.Second)
	defer stop()
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+c.host+"/", bytes.NewReader(body))
	if err != nil {
		return err
	}
	const contentType = "application/json; charset=utf-8"
	now := time.Now().UTC()
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-TC-Action", action)
	req.Header.Set("X-TC-Region", c.config.Region)
	req.Header.Set("X-TC-Version", tencentVersion)
	req.Header.Set("X-TC-Timestamp", strconv.FormatInt(now.Unix(), 10))
	req.Header.Set("Authorization", c.sign(now, contentType, body))
	if c.config.Token != "" {
		req.Header.Set("X-TC-Token", c.config.Token)
	}

	res, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("WMA Tencent Cloud %s: %w", action, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("WMA Tencent Cloud %s: HTTP %d", action, res.StatusCode)
	}
	var envelope struct {
		Response json.RawMessage
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&envelope); err != nil || len(envelope.Response) == 0 {
		return fmt.Errorf("WMA Tencent Cloud %s: invalid response", action)
	}
	var failure struct {
		Error *struct{ Code string }
	}
	if err := json.Unmarshal(envelope.Response, &failure); err != nil {
		return fmt.Errorf("WMA Tencent Cloud %s: invalid response data", action)
	}
	if failure.Error != nil {
		if errorCode.MatchString(failure.Error.Code) {
			return fmt.Errorf("WMA Tencent Cloud %s: %s", action, failure.Error.Code)
		}
		return fmt.Errorf("WMA Tencent Cloud %s rejected the request", action)
	}
	if err := json.Unmarshal(envelope.Response, output); err != nil {
		return fmt.Errorf("WMA Tencent Cloud %s: invalid response data", action)
	}
	return nil
}

// sign implements the Tencent Cloud TC3-HMAC-SHA256 signature for a JSON POST.
func (c *tencentCloud) sign(now time.Time, contentType string, body []byte) string {
	const signed = "content-type;host"
	digest := func(payload []byte) string {
		sum := sha256.Sum256(payload)
		return hex.EncodeToString(sum[:])
	}
	mac := func(key []byte, value string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(value))
		return h.Sum(nil)
	}
	day := now.Format(time.DateOnly)
	scope := day + "/" + tencentService + "/tc3_request"
	canonical := "POST\n/\n\ncontent-type:" + contentType + "\nhost:" + c.host + "\n\n" + signed + "\n" + digest(body)
	message := fmt.Sprintf("TC3-HMAC-SHA256\n%d\n%s\n%s", now.Unix(), scope, digest([]byte(canonical)))
	key := mac([]byte("TC3"+c.config.SecretKey), day)
	key = mac(mac(key, tencentService), "tc3_request")
	return fmt.Sprintf("TC3-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%x", c.config.SecretID, scope, signed, mac(key, message))
}
