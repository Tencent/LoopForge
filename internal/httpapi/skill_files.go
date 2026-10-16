package httpapi

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

// skillUploadBodyLimit bounds one multipart upload request (64MB of file
// content plus multipart framing overhead).
const skillUploadBodyLimit = domain.SkillTotalMaxBytes + 1<<20

// skillFiles handles POST /api/skills/:id/files — a multipart upload of one
// or more files (or a .zip archive that is unpacked server-side). File parts
// use the name "files"; because Go's multipart parser keeps only the
// basename, folder uploads carry the relative path in a parallel "paths"
// value field (one entry per file part, same order). A shared top-level
// directory (folder pick or zipped folder) is stripped.
func (h *catalogHandlers) skillFiles(c *gin.Context) {
	skillID := strings.TrimSpace(c.Param("id"))
	skill, ok := h.repo.Skill(skillID)
	if !ok {
		c.JSON(http.StatusNotFound, map[string]string{"error": "skill not found"})
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, skillUploadBodyLimit)
	if err := c.Request.ParseMultipartForm(32 << 20); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "上传内容无效或超过 65MB 请求上限"})
		return
	}
	form, _ := c.MultipartForm()
	headers := form.File["files"]
	if len(headers) == 0 {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "缺少名为 files 的上传文件"})
		return
	}
	logicalPaths := form.Value["paths"]
	if len(logicalPaths) > 0 && len(logicalPaths) != len(headers) {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "paths 与 files 数量不一致"})
		return
	}

	// Existing state for collision checks only (replace semantics wipe
	// existing binary rows server-side, so no overwrite accounting is
	// needed here).
	refs := h.repo.SkillFileRefs(skillID)
	_ = refs

	var inputs []repo.SkillFileInput
	seen := map[string]bool{}
	reject := func(err error) {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	takePath := func(path string) (string, error) {
		if _, isText := skill.Files[path]; isText {
			return "", fmt.Errorf("路径 %s 与文本附加文件冲突", path)
		}
		if seen[path] {
			return "", fmt.Errorf("上传中存在重复路径 %s", path)
		}
		seen[path] = true
		return path, nil
	}

	// Resolve logical names; zip parts are extracted immediately (their
	// paths are final), plain parts are collected for top-dir stripping.
	type plainPart struct {
		path   string
		header *multipart.FileHeader
	}
	var plain []plainPart
	for i, header := range headers {
		name := header.Filename
		if i < len(logicalPaths) && strings.TrimSpace(logicalPaths[i]) != "" {
			name = logicalPaths[i]
		}
		if strings.EqualFold(path.Ext(name), ".zip") {
			extracted, err := readSkillZip(header, seen)
			if err != nil {
				reject(err)
				return
			}
			inputs = append(inputs, extracted...)
			continue
		}
		clean, err := cleanUploadPath(name)
		if err != nil {
			reject(err)
			return
		}
		plain = append(plain, plainPart{path: clean, header: header})
	}

	// Folder picks arrive as "my-folder/..." for every file; strip the
	// shared top-level directory the same way zip unpacking does.
	plainNames := make([]string, len(plain))
	for i, part := range plain {
		plainNames[i] = part.path
	}
	if _, nested := sharedTopDir(plainNames); nested {
		for i := range plain {
			stripped := strings.Join(strings.Split(plain[i].path, "/")[1:], "/")
			clean, err := cleanUploadPath(stripped)
			if err != nil {
				reject(err)
				return
			}
			plain[i].path = clean
		}
	}

	for _, part := range plain {
		if _, err := takePath(part.path); err != nil {
			c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		content, err := readBoundedPart(part.header)
		if err != nil {
			reject(err)
			return
		}
		inputs = append(inputs, repo.SkillFileInput{Path: part.path, Content: content})
	}

	// Replace semantics: the uploaded batch is the new complete file list.
	// Limit checks run against the incoming batch only; text Files still
	// count toward the per-skill cap.
	var incomingBytes int64
	for _, in := range inputs {
		incomingBytes += int64(len(in.Content))
	}
	if int64(len(skill.Files))+int64(len(inputs)) > domain.SkillMaxFileCount {
		c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("单个技能最多 %d 个文件", domain.SkillMaxFileCount)})
		return
	}
	if incomingBytes > domain.SkillTotalMaxBytes {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "单个技能文件总量超过 64MB 限制"})
		return
	}

	if err := h.repo.PutSkillFiles(skillID, inputs); err != nil {
		respond(c, err)
		return
	}
	saved, _ := h.repo.Skill(skillID)
	c.JSON(http.StatusOK, saved)
}

// skillFile handles GET/DELETE /api/skills/:id/files/*path — download or
// remove one binary skill file.
func (h *catalogHandlers) skillFile(c *gin.Context) {
	skillID := strings.TrimSpace(c.Param("id"))
	filePath := strings.TrimPrefix(c.Param("path"), "/")
	if _, ok := h.repo.Skill(skillID); !ok {
		c.JSON(http.StatusNotFound, map[string]string{"error": "skill not found"})
		return
	}
	if err := domain.ValidateRelativeFilePath(filePath); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	switch c.Request.Method {
	case http.MethodGet:
		content, ok := h.repo.SkillFileContent(skillID, filePath)
		if !ok {
			c.JSON(http.StatusNotFound, map[string]string{"error": "file not found"})
			return
		}
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", pathEscapeRFC5987(path.Base(filePath))))
		c.Data(http.StatusOK, "application/octet-stream", content)
	case http.MethodDelete:
		if err := h.repo.DeleteSkillFile(skillID, filePath); err != nil {
			respond(c, err)
			return
		}
		saved, _ := h.repo.Skill(skillID)
		c.JSON(http.StatusOK, saved)
	default:
		c.JSON(http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// cleanUploadPath validates a multipart part filename as a relative skill
// path. Folder picks arrive with slash-separated relative paths.
func cleanUploadPath(name string) (string, error) {
	clean := strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	clean = strings.TrimPrefix(clean, "./")
	if err := domain.ValidateRelativeFilePath(clean); err != nil {
		return "", fmt.Errorf("无效文件路径 %q", name)
	}
	return clean, nil
}

// readBoundedPart reads one multipart file part enforcing the per-file cap.
func readBoundedPart(header *multipart.FileHeader) ([]byte, error) {
	file, err := header.Open()
	if err != nil {
		return nil, fmt.Errorf("无法读取上传文件 %s", header.Filename)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, domain.SkillFileMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("无法读取上传文件 %s", header.Filename)
	}
	if int64(len(content)) > domain.SkillFileMaxBytes {
		return nil, fmt.Errorf("文件 %s 超过 1MB 限制", header.Filename)
	}
	return content, nil
}

// readSkillZip unpacks one .zip part into skill file inputs. It rejects path
// traversal and non-regular entries (symlinks), strips a shared top-level
// directory and macOS metadata noise, and enforces the size caps while
// decompressing.
func readSkillZip(header *multipart.FileHeader, seen map[string]bool) ([]repo.SkillFileInput, error) {
	file, err := header.Open()
	if err != nil {
		return nil, fmt.Errorf("无法读取 zip %s", header.Filename)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, skillUploadBodyLimit))
	if err != nil || len(raw) == 0 {
		return nil, fmt.Errorf("无法读取 zip %s", header.Filename)
	}
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("%s 不是有效的 zip 文件", header.Filename)
	}

	names := make([]string, 0, len(reader.File))
	files := make(map[string]*zip.File)
	for _, f := range reader.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if !f.Mode().IsRegular() { // reject symlinks and other non-regular entries
			return nil, fmt.Errorf("zip 内含非常规文件条目 %s", f.Name)
		}
		name := strings.ReplaceAll(f.Name, "\\", "/")
		name = strings.TrimPrefix(path.Clean(name), "./")
		if top(name) == "__MACOSX" || path.Base(name) == ".DS_Store" {
			continue
		}
		if err := domain.ValidateRelativeFilePath(name); err != nil {
			return nil, fmt.Errorf("zip 内含无效路径 %q", f.Name)
		}
		names = append(names, name)
		files[name] = f
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("zip %s 中没有可用的文件", header.Filename)
	}
	// Strip a single shared top-level directory: zipping a folder produces
	// "my-skill/..." for every entry.
	if _, nested := sharedTopDir(names); nested {
		stripped := make([]string, 0, len(names))
		for _, name := range names {
			short := strings.Join(strings.Split(name, "/")[1:], "/")
			if err := domain.ValidateRelativeFilePath(short); err != nil {
				return nil, fmt.Errorf("zip 内含无效路径 %q", name)
			}
			files[short] = files[name]
			delete(files, name)
			stripped = append(stripped, short)
		}
		names = stripped
	}

	var out []repo.SkillFileInput
	var total int64
	for _, name := range names {
		if seen[name] {
			return nil, fmt.Errorf("上传中存在重复路径 %s", name)
		}
		rc, err := files[name].Open()
		if err != nil {
			return nil, fmt.Errorf("无法解压 %s", name)
		}
		content, err := io.ReadAll(io.LimitReader(rc, domain.SkillFileMaxBytes+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("无法解压 %s", name)
		}
		if int64(len(content)) > domain.SkillFileMaxBytes {
			return nil, fmt.Errorf("zip 内文件 %s 超过 1MB 限制", name)
		}
		total += int64(len(content))
		if total > domain.SkillTotalMaxBytes {
			return nil, fmt.Errorf("解压后总量超过 64MB 限制")
		}
		seen[name] = true
		out = append(out, repo.SkillFileInput{Path: name, Content: content, Mode: uint32(files[name].Mode().Perm())})
	}
	return out, nil
}

func top(name string) string {
	if idx := strings.Index(name, "/"); idx > 0 {
		return name[:idx]
	}
	return ""
}

// sharedTopDir reports whether every entry is nested under one identical
// top-level directory segment.
func sharedTopDir(names []string) (string, bool) {
	if len(names) == 0 {
		return "", false
	}
	first := top(names[0])
	if first == "" {
		return "", false
	}
	for _, name := range names[1:] {
		if top(name) != first {
			return "", false
		}
	}
	return first, true
}

// pathEscapeRFC5987 encodes a filename for the Content-Disposition header.
func pathEscapeRFC5987(name string) string {
	var b strings.Builder
	for _, r := range []byte(name) {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.IndexByte("-._~", r) >= 0 {
			b.WriteByte(r)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", r)
	}
	return b.String()
}
