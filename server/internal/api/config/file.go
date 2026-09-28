package config

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/lx-wnk/kontor/server/internal/cmdscope"
)

// maxConfigFileBytes caps both the read response and the accepted write payload.
const maxConfigFileBytes = 1 * 1024 * 1024 // 1 MB

type fileResponse struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	MTime    int64  `json:"mtime"`
	Editable bool   `json:"editable"`
	Source   string `json:"source"`
}

type filePutRequest struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	BaseMTime int64  `json:"baseMtime"`
}

type filePutResponse struct {
	Path  string `json:"path"`
	MTime int64  `json:"mtime"`
	Size  int64  `json:"size"`
}

// File handles GET /api/config/file?path=&spawnerId=&sessionId=&cwd=.
// It returns the content of an enumerated, editable config file. The scope is
// resolved exactly as the enumeration endpoints, and the requested path is
// authorized only if it is a member of that scope's editable set.
func (h *Handler) File(w http.ResponseWriter, r *http.Request) {
	scope := h.resolve(r)
	reqPath := r.URL.Query().Get("path")
	if reqPath == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}

	canon, source, ok := h.resolveEditable(scope, reqPath)
	if !ok {
		http.Error(w, "path is not an editable config file in this scope", http.StatusForbidden)
		return
	}

	info, err := os.Stat(canon)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	if info.Size() > maxConfigFileBytes {
		http.Error(w, "file exceeds 1 MB limit", http.StatusRequestEntityTooLarge)
		return
	}
	data, err := os.ReadFile(canon)
	if err != nil {
		http.Error(w, "read failed", http.StatusInternalServerError)
		return
	}

	writeJSON(w, fileResponse{
		Path:     canon,
		Content:  string(data),
		MTime:    info.ModTime().Unix(),
		Editable: true,
		Source:   source,
	})
}

// SaveFile handles PUT /api/config/file?spawnerId=&sessionId=&cwd= with a JSON
// body {path, content, baseMtime}. Scope params travel as query string (same as
// the GET) so resolution is identical. The write is authorized only if the path
// is in the re-resolved editable set; it is rejected on concurrent modification
// (409) and oversize content (413), and is applied atomically.
func (h *Handler) SaveFile(w http.ResponseWriter, r *http.Request) {
	var req filePutRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxConfigFileBytes+4096)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Path == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	if int64(len(req.Content)) > maxConfigFileBytes {
		http.Error(w, "content exceeds 1 MB limit", http.StatusRequestEntityTooLarge)
		return
	}

	scope := h.resolve(r)
	canon, _, ok := h.resolveEditable(scope, req.Path)
	if !ok {
		http.Error(w, "path is not an editable config file in this scope", http.StatusForbidden)
		return
	}

	info, err := os.Stat(canon)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	// Optimistic concurrency: reject if the file changed since the client loaded
	// it. baseMtime == 0 opts out of the check (e.g. force save).
	if req.BaseMTime != 0 && info.ModTime().Unix() != req.BaseMTime {
		http.Error(w, "file modified since it was loaded", http.StatusConflict)
		return
	}

	mode := info.Mode().Perm()
	if mode == 0 {
		mode = 0o644
	}
	if err := atomicWrite(canon, []byte(req.Content), mode); err != nil {
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}

	newInfo, err := os.Stat(canon)
	if err != nil {
		http.Error(w, "stat after write failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, filePutResponse{
		Path:  canon,
		MTime: newInfo.ModTime().Unix(),
		Size:  newInfo.Size(),
	})
}

// resolveEditable canonicalizes reqPath and looks it up in the scope's editable
// allow-list. ok is false for any path that is not an enumerated, editable file
// — including traversal and symlink-escape attempts, which canonicalize to a
// path that is simply not a key in the set.
func (h *Handler) resolveEditable(scope cmdscope.Scope, reqPath string) (canon, source string, ok bool) {
	c, err := canonical(reqPath)
	if err != nil {
		return "", "", false
	}
	source, ok = h.editableFiles(scope)[c]
	return c, source, ok
}

// editableFiles builds the write allow-list for a scope: a map from the
// canonicalized absolute path of every editable skill/command/memory file to
// its source layer ("user" | "project"). Enumeration IS the allow-list.
func (h *Handler) editableFiles(scope cmdscope.Scope) map[string]string {
	out := map[string]string{}
	add := func(path, source string) {
		if path == "" || !cmdscope.IsEditableSource(source) {
			return
		}
		if c, err := canonical(path); err == nil {
			out[c] = source
		}
	}
	for _, s := range scope.Skills() {
		add(s.Path, s.Source)
	}
	for _, c := range scope.CommandDetails() {
		add(c.Path, c.Source)
	}
	for _, m := range enumerateMemoryFiles(scope) {
		add(m.Path, m.Scope)
	}
	return out
}

// canonical resolves path to an absolute, symlink-free form. EvalSymlinks
// requires the file to exist, which enforces the v1 rule: only existing files
// may be read or written (no create).
func canonical(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// atomicWrite writes data to a temp file in the target's directory, fsyncs via
// close, restores the original mode, then renames over the target so a reader
// never observes a partial file.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-edit-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeds

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
