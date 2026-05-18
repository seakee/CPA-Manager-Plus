package panel

import (
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
)

type Service struct {
	PanelPath string
	Embedded  fs.FS
}

func New(panelPath string, embedded fs.FS) *Service {
	return &Service{PanelPath: panelPath, Embedded: embedded}
}

func (s *Service) ServeManagementHTML(w http.ResponseWriter, writeError func(http.ResponseWriter, int, error)) {
	if s.PanelPath != "" {
		if file, err := os.Open(s.PanelPath); err == nil {
			defer file.Close()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.Copy(w, file)
			return
		}
	}
	data, err := fs.ReadFile(s.Embedded, "web/management.html")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", mime.TypeByExtension(".html"))
	_, _ = w.Write(data)
}
