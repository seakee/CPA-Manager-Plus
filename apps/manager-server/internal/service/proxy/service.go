package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type Service struct {
	managerConfigService *managerconfig.Service
}

const cpaPluginResourcePrefix = "/v0/resource/plugins"

func New(managerConfigService *managerconfig.Service) *Service {
	return &Service{managerConfigService: managerConfigService}
}

func (s *Service) ProxyManagement(w http.ResponseWriter, r *http.Request, writeError func(http.ResponseWriter, int, error)) {
	s.proxyWithSavedManagementKey(w, r, writeError)
}

func (s *Service) ProxyCPA(w http.ResponseWriter, r *http.Request, writeError func(http.ResponseWriter, int, error)) {
	s.proxyWithSavedManagementKey(w, r, writeError)
}

func (s *Service) proxyWithSavedManagementKey(w http.ResponseWriter, r *http.Request, writeError func(http.ResponseWriter, int, error)) {
	setup, ok, err := s.resolveSetup(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeError(w, http.StatusPreconditionRequired, errors.New("usage service is not configured"))
		return
	}
	target, err := url.Parse(setup.CPAUpstreamURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host
		req.Header.Set("Authorization", "Bearer "+setup.ManagementKey)
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		writeError(w, http.StatusBadGateway, err)
	}
	proxy.ServeHTTP(w, r)
}

func (s *Service) ProxyModelList(w http.ResponseWriter, r *http.Request, writeError func(http.ResponseWriter, int, error), methodNotAllowed func(http.ResponseWriter)) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	setup, ok, err := s.resolveSetup(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeError(w, http.StatusPreconditionRequired, errors.New("usage service is not configured"))
		return
	}
	target, err := url.Parse(setup.CPAUpstreamURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		writeError(w, http.StatusBadGateway, err)
	}
	proxy.ServeHTTP(w, r)
}

func IsModelListPath(path string) bool {
	cleaned := strings.TrimRight(path, "/")
	return cleaned == "/v1/models" || cleaned == "/models"
}

func IsCPAProxyPath(path string) bool {
	cleaned := strings.TrimRight(path, "/")
	if cleaned == "" {
		return false
	}
	if _, ok := exactCPAProxyPaths[cleaned]; ok {
		return true
	}
	for _, prefix := range cpaProxyPathPrefixes {
		if cleaned == prefix || strings.HasPrefix(cleaned, prefix+"/") {
			return true
		}
	}
	return false
}

func IsCPAPluginResourcePath(path string) bool {
	cleaned := strings.TrimRight(path, "/")
	return cleaned == cpaPluginResourcePrefix || strings.HasPrefix(cleaned, cpaPluginResourcePrefix+"/")
}

var exactCPAProxyPaths = map[string]struct{}{
	"/ampcode":                             {},
	"/api-call":                            {},
	"/api-key-usage":                       {},
	"/api-keys":                            {},
	"/anthropic-auth-url":                  {},
	"/antigravity-auth-url":                {},
	"/claude-api-key":                      {},
	"/codex-api-key":                       {},
	"/codex-auth-url":                      {},
	"/config":                              {},
	"/config.yaml":                         {},
	"/debug":                               {},
	"/force-model-prefix":                  {},
	"/gemini-api-key":                      {},
	"/gemini-cli-auth-url":                 {},
	"/get-auth-status":                     {},
	"/latest-version":                      {},
	"/logging-to-file":                     {},
	"/logs":                                {},
	"/logs-max-total-size-mb":              {},
	"/oauth-callback":                      {},
	"/oauth-excluded-models":               {},
	"/oauth-model-alias":                   {},
	"/openai-compatibility":                {},
	"/plugin-store":                        {},
	"/plugins":                             {},
	"/proxy-url":                           {},
	"/quota-exceeded/switch-preview-model": {},
	"/quota-exceeded/switch-project":       {},
	"/request-error-logs":                  {},
	"/request-log":                         {},
	"/request-retry":                       {},
	"/routing/strategy":                    {},
	"/vertex-api-key":                      {},
	"/vertex/import":                       {},
	"/ws-auth":                             {},
	"/xai-auth-url":                        {},
}

var cpaProxyPathPrefixes = []string{
	"/ampcode/",
	"/auth-files",
	"/oauth-excluded-models/",
	"/oauth-model-alias/",
	"/plugin-store",
	"/plugins",
	cpaPluginResourcePrefix,
	"/request-error-logs",
	"/request-log-by-id",
}

func (s *Service) resolveSetup(ctx context.Context) (store.Setup, bool, error) {
	return s.managerConfigService.ResolveSetup(ctx)
}
