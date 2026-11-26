package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

const MaxSOPArtifactBytes = 20 << 20
const MaxInlineRunnerArtifactBytes = 1 << 20

var ErrArtifactTooLarge = errors.New("artifact exceeds the file size limit")

type ArtifactBlob struct {
	ID     string `json:"id"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func RunnerOutputBlobID(taskID, path string) string {
	sum := sha256.Sum256([]byte(taskID + "\x00" + path))
	return hex.EncodeToString(sum[:])
}
