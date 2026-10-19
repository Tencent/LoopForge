package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGenerateRIOHeaders(t *testing.T) {
	paasID := "test_paas"
	appToken := "test_secret_key"
	timestamp := "1700000000"
	nonce := "4321"

	headers := GenerateRIOHeaders(paasID, appToken, timestamp, nonce)

	if headers["x-rio-paasid"] != paasID {
		t.Errorf("expected x-rio-paasid %q, got %q", paasID, headers["x-rio-paasid"])
	}
	if headers["x-rio-nonce"] != nonce {
		t.Errorf("expected x-rio-nonce %q, got %q", nonce, headers["x-rio-nonce"])
	}
	if headers["x-rio-timestamp"] != timestamp {
		t.Errorf("expected x-rio-timestamp %q, got %q", timestamp, headers["x-rio-timestamp"])
	}

	expectedStr := timestamp + appToken + nonce + timestamp
	sum := sha256.Sum256([]byte(expectedStr))
	expectedSig := strings.ToUpper(hex.EncodeToString(sum[:]))

	if headers["x-rio-signature"] != expectedSig {
		t.Errorf("expected signature %q, got %q", expectedSig, headers["x-rio-signature"])
	}
}

func TestSignAndParseSignedUser(t *testing.T) {
	secret := "super-secure-session-secret"
	user := &User{
		StaffID:     998877,
		LoginName:   "developer",
		ChineseName: "开发者",
		DeptID:      42,
		DeptName:    "研发中心",
		IsGuest:     false,
		AuthTime:    time.Now().Truncate(time.Second),
	}

	token, err := SignUser(user, secret)
	if err != nil {
		t.Fatalf("SignUser failed: %v", err)
	}

	parsed, err := ParseSignedUser(token, secret)
	if err != nil {
		t.Fatalf("ParseSignedUser failed: %v", err)
	}

	if parsed.StaffID != user.StaffID || parsed.LoginName != user.LoginName || parsed.ChineseName != user.ChineseName {
		t.Errorf("parsed user mismatch: got %+v, want %+v", parsed, user)
	}

	// 篡改测试
	tampered := token + "bad"
	if _, err := ParseSignedUser(tampered, secret); err == nil {
		t.Errorf("expected error on tampered token, got nil")
	}

	// 错误密钥测试
	if _, err := ParseSignedUser(token, "wrong-secret"); err == nil {
		t.Errorf("expected error with wrong secret, got nil")
	}
}

func TestExchangeCode_Success(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Query().Get("code") != "valid_code_123" {
			t.Errorf("expected code=valid_code_123, got %s", r.URL.Query().Get("code"))
		}
		if r.Header.Get("x-rio-paasid") != "my_paas" {
			t.Errorf("expected x-rio-paasid=my_paas, got %s", r.Header.Get("x-rio-paasid"))
		}
		if r.Header.Get("x-rio-signature") == "" {
			t.Errorf("expected non-empty signature")
		}

		resp := IOAAuthResponse{
			Ret:     0,
			ErrCode: 0,
			ErrMsg:  "",
			Data: &IOAAuthData{
				StaffId:     12345,
				LoginName:   "example-maintainer",
				ChineseName: "胡克强",
				DeptId:      678,
				DeptName:    "技术工程事业群",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	client := NewClient(Config{
		PaaSID:   "my_paas",
		AppToken: "my_token",
		AuthHost: mockServer.URL,
		AuthURI:  "/ebus/tof4/api/v1/passport/AccessToken",
	})
	client.SetHTTPClient(mockServer.Client())

	user, err := client.ExchangeCode(context.Background(), "valid_code_123")
	if err != nil {
		t.Fatalf("ExchangeCode returned unexpected error: %v", err)
	}

	if user.IsGuest {
		t.Errorf("expected non-guest user, got guest")
	}
	if user.StaffID != 12345 || user.LoginName != "example-maintainer" || user.ChineseName != "胡克强" {
		t.Errorf("user mismatch: %+v", user)
	}
}

func TestExchangeCode_Degradation(t *testing.T) {
	t.Run("missing credentials falls back to guest", func(t *testing.T) {
		client := NewClient(Config{
			PaaSID:   "",
			AppToken: "",
		})
		user, err := client.ExchangeCode(context.Background(), "some_code")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if !user.IsGuest || user.ChineseName != "访客" {
			t.Errorf("expected guest user, got %+v", user)
		}
	})

	t.Run("empty code falls back to guest", func(t *testing.T) {
		client := NewClient(Config{
			PaaSID:   "p",
			AppToken: "t",
		})
		user, err := client.ExchangeCode(context.Background(), "")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if !user.IsGuest {
			t.Errorf("expected guest user, got %+v", user)
		}
	})

	t.Run("remote error falls back to guest", func(t *testing.T) {
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"Ret": 502, "ErrMsg": "gateway error"}`))
		}))
		defer mockServer.Close()

		client := NewClient(Config{
			PaaSID:   "p",
			AppToken: "t",
			AuthHost: mockServer.URL,
		})
		client.SetHTTPClient(mockServer.Client())

		user, err := client.ExchangeCode(context.Background(), "any_code")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if !user.IsGuest {
			t.Errorf("expected guest fallback, got %+v", user)
		}
	})

	t.Run("invalid auth response falls back to guest", func(t *testing.T) {
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := IOAAuthResponse{
				Ret:    1001,
				ErrMsg: "code expired",
			}
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer mockServer.Close()

		client := NewClient(Config{
			PaaSID:   "p",
			AppToken: "t",
			AuthHost: mockServer.URL,
		})
		client.SetHTTPClient(mockServer.Client())

		user, err := client.ExchangeCode(context.Background(), "expired_code")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if !user.IsGuest {
			t.Errorf("expected guest fallback, got %+v", user)
		}
	})
}
