package auth

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestPhotoContentType(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	cases := []struct {
		name     string
		declared string
		data     []byte
		want     string
	}{
		{"declared image kept", "image/jpeg", png, "image/jpeg"},
		{"empty sniffs content", "", png, "image/png"},
		{"text sniffs content", "text/plain; charset=utf-8", png, "image/png"},
		{"json sniffs content", "application/json", png, "image/png"},
		{"non image declared falls back", "application/octet-stream", []byte("x"), "image/png"},
		{"sniffed non image falls back", "", []byte("hello world"), "image/png"},
		{"sniffed gif kept", "", []byte("GIF89a......"), "image/gif"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := photoContentType(tc.declared, tc.data); got != tc.want {
				t.Fatalf("photoContentType(%q) = %q, want %q", tc.declared, got, tc.want)
			}
		})
	}
}

func photoResponse(status int, contentType, body string) *http.Response {
	header := http.Header{}
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}
}

func TestReadPhotoResponse(t *testing.T) {
	t.Run("not found status", func(t *testing.T) {
		if _, _, err := readPhotoResponse(photoResponse(http.StatusNotFound, "", "missing")); !errors.Is(err, ErrPhotoNotFound) {
			t.Fatalf("err = %v, want ErrPhotoNotFound", err)
		}
	})
	t.Run("unexpected status keeps body excerpt", func(t *testing.T) {
		_, _, err := readPhotoResponse(photoResponse(http.StatusBadGateway, "", "upstream down"))
		if err == nil || err.Error() != "unexpected status 502 from SSO photo: upstream down" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("empty body is not found", func(t *testing.T) {
		if _, _, err := readPhotoResponse(photoResponse(http.StatusOK, "image/png", "")); !errors.Is(err, ErrPhotoNotFound) {
			t.Fatalf("err = %v, want ErrPhotoNotFound", err)
		}
	})
	t.Run("ok returns data and normalized type", func(t *testing.T) {
		data, ct, err := readPhotoResponse(photoResponse(http.StatusOK, "text/html", "GIF89a......"))
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if string(data) != "GIF89a......" || ct != "image/gif" {
			t.Fatalf("data=%q ct=%q", data, ct)
		}
	})
}
