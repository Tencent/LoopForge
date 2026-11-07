package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"
)

func serveBinaryArtifact(c *gin.Context, path, name string) {
	// Paths come from server configuration or the build cache. Constrain the
	// final file open to that directory so symlinks cannot escape it.
	file, err := os.OpenInRoot(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "binary is not available"})
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "binary is not available"})
		return
	}
	if c.Query("metadata") == "1" {
		serveBinaryMetadata(c, file)
		return
	}
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	http.ServeContent(c.Writer, c.Request, name, info.ModTime(), file)
}

func serveBinaryMetadata(c *gin.Context, file io.Reader) {
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "hash binary: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"sha256": hex.EncodeToString(hash.Sum(nil))})
}
