package agents

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/lx-wnk/kontor/server/internal/apierr"
)

const (
	// maxUploadImageBytes caps the accepted multipart body — enforced via
	// http.MaxBytesReader so oversized uploads are rejected before they are
	// fully buffered.
	maxUploadImageBytes = 10 << 20 // 10 MiB
	uploadImageDirPerm  = 0o700
	uploadImageFilePerm = 0o600
)

// UploadImageHandler handles POST /api/agents/{pid}/upload-image. It accepts a
// single image file and saves it under a stable temp directory (never the
// user's project cwd) so the returned absolute path can be injected into a
// live prompt as an "@<path>" image reference.
type UploadImageHandler struct{}

// NewUploadImageHandler creates an UploadImageHandler. It has no dependencies —
// uploads are namespaced by PID under a fixed OS-temp base directory.
func NewUploadImageHandler() *UploadImageHandler {
	return &UploadImageHandler{}
}

// uploadImageBaseDir returns (and creates) the stable base directory uploaded
// images are stored under.
//
// The directory name carries the UID, matching channelconfig's convention: on
// Linux os.TempDir() is the shared /tmp, so a fixed name lets any local user
// pre-create the directory with permissive modes — MkdirAll then succeeds on it
// and the user's screenshots are readable by them. O_EXCL and the random file
// name protect the file, not the directory it lives in.
func uploadImageBaseDir() (string, error) {
	dir := filepath.Join(os.TempDir(), "agent-dashboard-uploads-"+strconv.Itoa(os.Getuid()))
	if err := os.MkdirAll(dir, uploadImageDirPerm); err != nil {
		return "", err
	}
	return dir, nil
}

// randomUploadFilename returns an unguessable filename preserving ext (which
// must include the leading dot, e.g. ".png", or be empty).
func randomUploadFilename(ext string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]) + ext, nil
}

// UploadImage handles POST /api/agents/{pid}/upload-image. The pid identifies
// the target session only for namespacing the stored file — no live process
// lookup is required. Rejects non-image/* content types and bodies over
// maxUploadImageBytes. Responds 200 with {"path": "/abs/path/to/file.ext"}.
func (h *UploadImageHandler) UploadImage(w http.ResponseWriter, r *http.Request) {
	pid, err := strconv.Atoi(r.PathValue("pid"))
	if err != nil || pid <= 0 {
		apierr.JSONError(w, http.StatusBadRequest, "invalid pid")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadImageBytes)
	if err := r.ParseMultipartForm(maxUploadImageBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			apierr.JSONError(w, http.StatusRequestEntityTooLarge, "image exceeds 10 MiB limit")
			return
		}
		apierr.JSONError(w, http.StatusBadRequest, "malformed multipart body")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll() //nolint:errcheck
	}

	file, header, err := r.FormFile("image")
	if err != nil {
		apierr.JSONError(w, http.StatusBadRequest, "missing image file field")
		return
	}
	defer file.Close()

	mediaType := mediaTypeOf(header.Header.Get("Content-Type"))
	if !strings.HasPrefix(mediaType, "image/") {
		apierr.JSONError(w, http.StatusUnsupportedMediaType, "file must be an image")
		return
	}

	dir, err := uploadImageBaseDir()
	if err != nil {
		slog.Error("upload-image: base dir", "err", err)
		apierr.JSONError(w, http.StatusInternalServerError, "could not prepare upload directory")
		return
	}
	pidDir := filepath.Join(dir, strconv.Itoa(pid))
	if err := os.MkdirAll(pidDir, uploadImageDirPerm); err != nil {
		slog.Error("upload-image: pid dir", "err", err)
		apierr.JSONError(w, http.StatusInternalServerError, "could not prepare upload directory")
		return
	}

	name, err := randomUploadFilename(extensionFor(header.Filename, mediaType))
	if err != nil {
		slog.Error("upload-image: filename", "err", err)
		apierr.JSONError(w, http.StatusInternalServerError, "could not generate filename")
		return
	}
	path := filepath.Join(pidDir, name)

	dst, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, uploadImageFilePerm)
	if err != nil {
		slog.Error("upload-image: create file", "err", err)
		apierr.JSONError(w, http.StatusInternalServerError, "could not save file")
		return
	}
	defer dst.Close()

	if _, err := io.Copy(dst, file); err != nil {
		_ = os.Remove(path)
		slog.Error("upload-image: write file", "err", err)
		apierr.JSONError(w, http.StatusInternalServerError, "could not save file")
		return
	}

	apierr.WriteJSON(w, http.StatusOK, map[string]string{"path": path})
}

// mediaTypeOf parses a Content-Type header value down to its bare media type
// (e.g. "image/png; charset=binary" -> "image/png"), tolerating an empty or
// unparsable header by returning "application/octet-stream" (never image/*).
func mediaTypeOf(contentType string) string {
	if contentType == "" {
		return "application/octet-stream"
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "application/octet-stream"
	}
	return mediaType
}

// safeExtRe is the only shape accepted from a client-supplied filename. Two
// concrete failures it prevents: a name like "shot.png (1)" yields the extension
// ".png (1)", and the stored path then contains a space — the injected `@<path>`
// token breaks there and the remainder lands in the prompt as literal text; and
// slicing a non-ASCII extension to a byte length can cut mid-rune, producing a
// name the filesystem rejects.
var safeExtRe = regexp.MustCompile(`^\.[A-Za-z0-9]{1,8}$`)

// extensionFor prefers the extension from the original filename and falls back
// to one derived from mediaType whenever that extension is absent or not a
// plain, short, alphanumeric one.
func extensionFor(filename, mediaType string) string {
	ext := filepath.Ext(filename)
	if safeExtRe.MatchString(ext) {
		return ext
	}
	if exts, err := mime.ExtensionsByType(mediaType); err == nil && len(exts) > 0 {
		if safeExtRe.MatchString(exts[0]) {
			return exts[0]
		}
	}
	return ""
}
