package router

import (
	"log"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/app"
	apikeyaliascontroller "github.com/seakee/cpa-manager-plus/usage-service/internal/http/controller/apikeyalias"
	healthcontroller "github.com/seakee/cpa-manager-plus/usage-service/internal/http/controller/health"
	managerconfigcontroller "github.com/seakee/cpa-manager-plus/usage-service/internal/http/controller/managerconfig"
	modelpricecontroller "github.com/seakee/cpa-manager-plus/usage-service/internal/http/controller/modelprice"
	panelcontroller "github.com/seakee/cpa-manager-plus/usage-service/internal/http/controller/panel"
	proxycontroller "github.com/seakee/cpa-manager-plus/usage-service/internal/http/controller/proxy"
	setupcontroller "github.com/seakee/cpa-manager-plus/usage-service/internal/http/controller/setup"
	systemcontroller "github.com/seakee/cpa-manager-plus/usage-service/internal/http/controller/system"
	usagecontroller "github.com/seakee/cpa-manager-plus/usage-service/internal/http/controller/usage"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/http/middleware"
)

func NewGin(appCtx *app.Context) http.Handler {
	gin.SetMode(gin.ReleaseMode)

	healthHandler := &healthcontroller.Handler{ServiceID: appCtx.ServiceID}
	systemHandler := &systemcontroller.Handler{App: appCtx}
	setupHandler := &setupcontroller.Handler{App: appCtx}
	managerConfigHandler := &managerconfigcontroller.Handler{App: appCtx}
	usageHandler := &usagecontroller.Handler{App: appCtx}
	modelPriceHandler := &modelpricecontroller.Handler{App: appCtx}
	apiKeyAliasHandler := &apikeyaliascontroller.Handler{App: appCtx}
	proxyHandler := &proxycontroller.Handler{App: appCtx}
	panelHandler := &panelcontroller.Handler{App: appCtx}

	r := gin.New()
	r.Use(ginRecovery())
	r.Use(ginRequestLogger())

	r.Any("/health", gin.WrapF(middleware.WithCORS(appCtx.Config, healthHandler.Health)))
	r.Any("/status", gin.WrapF(middleware.WithCORS(appCtx.Config, systemHandler.Status)))
	r.Any("/usage-service/info", gin.WrapF(middleware.WithCORS(appCtx.Config, systemHandler.Info)))
	r.Any("/usage-service/config", gin.WrapF(middleware.WithCORS(appCtx.Config, managerConfigHandler.Handle)))
	r.Any("/setup", gin.WrapF(middleware.WithCORS(appCtx.Config, setupHandler.Setup)))
	r.Any("/management.html", gin.WrapF(panelHandler.ManagementHTML))
	r.GET("/", func(c *gin.Context) {
		http.Redirect(c.Writer, c.Request, "/management.html", http.StatusTemporaryRedirect)
	})
	r.OPTIONS("/", func(c *gin.Context) {
		middleware.WriteCORS(appCtx.Config, c.Writer, c.Request)
		c.Status(http.StatusNoContent)
	})

	r.Any("/v0/management/usage", gin.WrapF(middleware.WithCORS(appCtx.Config, usageHandler.Handle)))
	r.Any("/v0/management/usage/*path", gin.WrapF(middleware.WithCORS(appCtx.Config, usageHandler.Handle)))

	r.Any("/v0/management/model-prices", gin.WrapF(middleware.WithCORS(appCtx.Config, modelPriceHandler.Handle)))
	r.Any("/v0/management/model-prices/*path", gin.WrapF(middleware.WithCORS(appCtx.Config, modelPriceHandler.Handle)))

	r.Any("/v0/management/api-key-aliases", gin.WrapF(middleware.WithCORS(appCtx.Config, apiKeyAliasHandler.Handle)))
	r.Any("/v0/management/api-key-aliases/*path", gin.WrapF(middleware.WithCORS(appCtx.Config, apiKeyAliasHandler.Handle)))

	r.Any("/v1/models", ginProxyHandler(appCtx, proxyHandler.ModelList))
	r.Any("/models", ginProxyHandler(appCtx, proxyHandler.ModelList))

	r.NoRoute(func(c *gin.Context) {
		if c.Request.Method == http.MethodOptions {
			middleware.WriteCORS(appCtx.Config, c.Writer, c.Request)
			c.Status(http.StatusNoContent)
			return
		}
		if strings.HasPrefix(c.Request.URL.Path, "/v0/management/") {
			ginProxyHandler(appCtx, proxyHandler.Management)(c)
			return
		}
		http.NotFound(c.Writer, c.Request)
	})

	return r
}

func ginProxyHandler(appCtx *app.Context, handler http.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		writer := noCloseNotifyWriter{ResponseWriter: c.Writer}
		middleware.WithCORS(appCtx.Config, handler)(writer, c.Request)
	}
}

type noCloseNotifyWriter struct {
	http.ResponseWriter
}

func ginRecovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("panic serving %s %s: %v\n%s", c.Request.Method, c.Request.URL.Path, recovered, debug.Stack())
				c.AbortWithStatus(http.StatusInternalServerError)
			}
		}()
		c.Next()
	}
}

func ginRequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()
		log.Printf(
			"http %s %s status=%d duration=%s remote=%s",
			c.Request.Method,
			c.Request.URL.Path,
			c.Writer.Status(),
			time.Since(started),
			remoteAddr(c.Request.RemoteAddr),
		)
	}
}

func remoteAddr(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}
