package server

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

var gzipWriterPool = sync.Pool{
	New: func() any {
		w, _ := gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression)
		return w
	},
}

type gzipResponseWriter struct {
	gin.ResponseWriter
	gz          *gzip.Writer
	wroteHeader bool
	bypass      bool
}

func (g *gzipResponseWriter) shouldCompress() bool {
	ct := strings.ToLower(g.Header().Get("Content-Type"))
	if ct == "" {
		return true
	}
	if strings.HasPrefix(ct, "image/") ||
		strings.HasPrefix(ct, "video/") ||
		strings.HasPrefix(ct, "audio/") ||
		strings.Contains(ct, "text/event-stream") ||
		strings.Contains(ct, "application/zip") ||
		strings.Contains(ct, "application/octet-stream") {
		return false
	}
	return true
}

func (g *gzipResponseWriter) WriteHeader(code int) {
	if g.wroteHeader {
		return
	}
	g.wroteHeader = true
	if code == http.StatusNoContent || code == http.StatusNotModified || !g.shouldCompress() {
		g.bypass = true
		g.ResponseWriter.WriteHeader(code)
		return
	}
	g.Header().Del("Content-Length")
	g.Header().Set("Content-Encoding", "gzip")
	g.Header().Add("Vary", "Accept-Encoding")
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipResponseWriter) Write(data []byte) (int, error) {
	if !g.wroteHeader {
		if g.Header().Get("Content-Type") == "" {
			g.Header().Set("Content-Type", http.DetectContentType(data))
		}
		g.WriteHeader(http.StatusOK)
	}
	if g.bypass {
		return g.ResponseWriter.Write(data)
	}
	return g.gz.Write(data)
}

func (g *gzipResponseWriter) WriteString(s string) (int, error) {
	return g.Write([]byte(s))
}

func (g *gzipResponseWriter) Flush() {
	if !g.bypass && g.wroteHeader {
		_ = g.gz.Flush()
	}
	g.ResponseWriter.Flush()
}

func gzipMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodHead ||
			!strings.Contains(c.GetHeader("Accept-Encoding"), "gzip") ||
			strings.Contains(c.GetHeader("Connection"), "Upgrade") {
			c.Next()
			return
		}
		p := strings.ToLower(c.Request.URL.Path)
		if strings.Contains(p, "/videos/") ||
			strings.Contains(p, "/images/") ||
			strings.HasSuffix(p, "/download") ||
			strings.HasSuffix(p, "/events") {
			c.Next()
			return
		}

		gz := gzipWriterPool.Get().(*gzip.Writer)
		gz.Reset(c.Writer)

		gw := &gzipResponseWriter{
			ResponseWriter: c.Writer,
			gz:             gz,
		}
		c.Writer = gw
		defer func() {
			if gw.wroteHeader && !gw.bypass {
				_ = gz.Close()
			}
			gz.Reset(io.Discard)
			gzipWriterPool.Put(gz)
		}()

		c.Next()
	}
}
