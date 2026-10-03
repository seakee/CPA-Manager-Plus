package panel

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	embeddedPanelPath          = "web/management.html"
	embeddedFaviconPath        = "web/favicon.ico"
	embeddedAppleTouchIconPath = "web/apple-touch-icon.png"
)

type Service struct {
	PanelPath string
	Embedded  fs.FS

	embeddedData        []byte
	embeddedErr         error
	embeddedETag        string
	embeddedContentType string

	faviconData []byte
	faviconErr  error
	faviconETag string

	appleTouchIconData []byte
	appleTouchIconErr  error
	appleTouchIconETag string
}

func New(panelPath string, embedded fs.FS) *Service {
	s := &Service{
		PanelPath:           panelPath,
		Embedded:            embedded,
		embeddedContentType: htmlContentType(),
	}
	if embedded == nil {
		s.embeddedErr = fs.ErrNotExist
		s.faviconErr = fs.ErrNotExist
		s.appleTouchIconErr = fs.ErrNotExist
		return s
	}
	s.embeddedData, s.embeddedErr = fs.ReadFile(embedded, embeddedPanelPath)
	if s.embeddedErr == nil {
		sum := sha256.Sum256(s.embeddedData)
		s.embeddedETag = `"` + hex.EncodeToString(sum[:16]) + `"`
	}

	s.faviconData, s.faviconErr = fs.ReadFile(embedded, embeddedFaviconPath)
	if s.faviconErr == nil {
		sum := sha256.Sum256(s.faviconData)
		s.faviconETag = `"` + hex.EncodeToString(sum[:16]) + `"`
	}

	s.appleTouchIconData, s.appleTouchIconErr = fs.ReadFile(embedded, embeddedAppleTouchIconPath)
	if s.appleTouchIconErr == nil {
		sum := sha256.Sum256(s.appleTouchIconData)
		s.appleTouchIconETag = `"` + hex.EncodeToString(sum[:16]) + `"`
	}
	return s
}

func (s *Service) ServeManagementHTML(w http.ResponseWriter, r *http.Request, writeError func(http.ResponseWriter, int, error)) {
	if s.PanelPath != "" {
		if file, err := os.Open(s.PanelPath); err == nil {
			defer file.Close()
			info, statErr := file.Stat()
			if statErr != nil {
				writeError(w, http.StatusInternalServerError, statErr)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			http.ServeContent(w, r, "management.html", info.ModTime(), file)
			return
		}
	}
	if s.embeddedErr != nil {
		writeError(w, http.StatusInternalServerError, s.embeddedErr)
		return
	}
	// The embedded panel is fixed for the process lifetime and has no real file
	// modification time, so a content hash is the only stable validator here.
	w.Header().Set("Content-Type", s.embeddedContentType)
	w.Header().Set("ETag", s.embeddedETag)
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "management.html", time.Time{}, bytes.NewReader(s.embeddedData))
}

func (s *Service) ServeFavicon(w http.ResponseWriter, r *http.Request, writeError func(http.ResponseWriter, int, error)) {
	s.serveEmbeddedAsset(w, r, "favicon.ico", "image/x-icon", s.faviconData, s.faviconErr, s.faviconETag, writeError)
}

func (s *Service) ServeAppleTouchIcon(w http.ResponseWriter, r *http.Request, writeError func(http.ResponseWriter, int, error)) {
	s.serveEmbeddedAsset(w, r, "apple-touch-icon.png", "image/png", s.appleTouchIconData, s.appleTouchIconErr, s.appleTouchIconETag, writeError)
}

func (s *Service) serveEmbeddedAsset(
	w http.ResponseWriter,
	r *http.Request,
	name string,
	contentType string,
	data []byte,
	readErr error,
	etag string,
	writeError func(http.ResponseWriter, int, error),
) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	if readErr != nil {
		writeError(w, http.StatusInternalServerError, readErr)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

func htmlContentType() string {
	contentType := mime.TypeByExtension(".html")
	if contentType == "" {
		contentType = "text/html"
	}
	if !strings.Contains(contentType, "charset=") {
		contentType += "; charset=utf-8"
	}
	return contentType
}
