package panel

import (
	"io/fs"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
)

type Service struct {
	PanelPath string
	Embedded  fs.FS
}

func New(panelPath string, embedded fs.FS) *Service {
	return &Service{PanelPath: panelPath, Embedded: embedded}
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
	data, err := fs.ReadFile(s.Embedded, "web/management.html")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	contentType := mime.TypeByExtension(".html")
	if !strings.Contains(contentType, "charset=") {
		contentType += "; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}
