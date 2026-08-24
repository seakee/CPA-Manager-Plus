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

const embeddedPanelPath = "web/management.html"

type Service struct {
	PanelPath string
	Embedded  fs.FS

	embeddedData        []byte
	embeddedErr         error
	embeddedETag        string
	embeddedContentType string
}

func New(panelPath string, embedded fs.FS) *Service {
	s := &Service{
		PanelPath:           panelPath,
		Embedded:            embedded,
		embeddedContentType: htmlContentType(),
	}
	if embedded == nil {
		s.embeddedErr = fs.ErrNotExist
		return s
	}
	s.embeddedData, s.embeddedErr = fs.ReadFile(embedded, embeddedPanelPath)
	if s.embeddedErr == nil {
		sum := sha256.Sum256(s.embeddedData)
		s.embeddedETag = `"` + hex.EncodeToString(sum[:16]) + `"`
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
