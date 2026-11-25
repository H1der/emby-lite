package image

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	stdimg "image"
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

type inflightCall struct {
	wg  sync.WaitGroup
	err error
}

// Pipeline provides pure-Go on-demand image resizing with content-addressed disk caching,
// singleflight deduplication, and cached directory statistics.
type Pipeline struct {
	cacheDir string

	inflightMu sync.Mutex
	inflight   map[string]*inflightCall

	statsMu      sync.RWMutex
	cachedStats  CacheStats
	statsExpires time.Time
}

type CacheStats struct {
	FileCount  int64  `json:"file_count"`
	TotalBytes int64  `json:"total_bytes"`
	CacheDir   string `json:"cache_dir"`
}

func NewPipeline(cacheRoot string) *Pipeline {
	dir := filepath.Join(cacheRoot, "images")
	_ = os.MkdirAll(dir, 0o755)
	return &Pipeline{
		cacheDir: dir,
		inflight: make(map[string]*inflightCall),
	}
}

// ServeFile serves an image with optional maxWidth/maxHeight resizing, disk caching, and ETag/304.
func (p *Pipeline) ServeFile(w http.ResponseWriter, r *http.Request, srcPath, tag string, maxWidth, maxHeight, quality int) error {
	if maxWidth < 0 {
		maxWidth = 0
	}
	if maxHeight < 0 {
		maxHeight = 0
	}
	if maxWidth > 3840 {
		maxWidth = 3840
	}
	if maxHeight > 2160 {
		maxHeight = 2160
	}
	if quality <= 0 || quality > 95 {
		quality = 85
	}

	// Fast path: no resize requested -> serve original file directly.
	if maxWidth == 0 && maxHeight == 0 {
		return serveWithETag(w, r, srcPath, tag)
	}

	variantKey := cacheKey(tag, maxWidth, maxHeight, quality)
	etag := `"` + variantKey + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}

	cachedPath := p.cachePath(variantKey)
	if _, err := os.Stat(cachedPath); err == nil {
		return serveWithETag(w, r, cachedPath, variantKey)
	}

	// Deduplicate concurrent renders of the exact same thumbnail variant.
	err := p.doSingleflight(variantKey, func() error {
		if _, err := os.Stat(cachedPath); err == nil {
			return nil
		}
		return p.renderToCache(srcPath, cachedPath, maxWidth, maxHeight, quality)
	})
	if err != nil {
		// Unrecognized or non-resizable image: gracefully fall back to original.
		if _, statErr := os.Stat(srcPath); statErr == nil {
			return serveWithETag(w, r, srcPath, tag)
		}
		return err
	}

	if _, err := os.Stat(cachedPath); err == nil {
		return serveWithETag(w, r, cachedPath, variantKey)
	}
	return serveWithETag(w, r, srcPath, tag)
}

func (p *Pipeline) doSingleflight(key string, fn func() error) error {
	p.inflightMu.Lock()
	if c, ok := p.inflight[key]; ok {
		p.inflightMu.Unlock()
		c.wg.Wait()
		return c.err
	}
	c := &inflightCall{}
	c.wg.Add(1)
	p.inflight[key] = c
	p.inflightMu.Unlock()

	c.err = fn()
	c.wg.Done()

	p.inflightMu.Lock()
	delete(p.inflight, key)
	p.inflightMu.Unlock()
	return c.err
}

func (p *Pipeline) renderToCache(srcPath, cachedPath string, maxWidth, maxHeight, quality int) error {
	srcFile, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	img, format, err := stdimg.Decode(srcFile)
	if err != nil {
		return err
	}

	bounds := img.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()
	dstW, dstH := fitDimensions(srcW, srcH, maxWidth, maxHeight)

	// Never upscale; signal caller to serve original.
	if dstW >= srcW && dstH >= srcH {
		return nil
	}

	dst := stdimg.NewRGBA(stdimg.Rect(0, 0, dstW, dstH))
	// BiLinear is ~5-8x faster than CatmullRom in pure Go with crisp poster quality.
	draw.BiLinear.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)

	var buf bytes.Buffer
	if strings.EqualFold(format, "png") && hasAlpha(img) {
		if err := png.Encode(&buf, dst); err != nil {
			return err
		}
	} else {
		if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: quality}); err != nil {
			return err
		}
	}

	return p.writeCacheFile(cachedPath, buf.Bytes())
}

// ServeRemoteURL fetches a remote image once into disk cache and serves it via the resize pipeline.
func (p *Pipeline) ServeRemoteURL(ctx context.Context, w http.ResponseWriter, r *http.Request, remoteURL, tag string, maxWidth, maxHeight, quality int) error {
	return p.ServeRemoteURLWithClient(ctx, w, r, remoteURL, tag, maxWidth, maxHeight, quality, nil)
}

// ServeRemoteURLWithClient fetches a remote image using a custom HTTP client (e.g. with proxy) and caches it.
func (p *Pipeline) ServeRemoteURLWithClient(ctx context.Context, w http.ResponseWriter, r *http.Request, remoteURL, tag string, maxWidth, maxHeight, quality int, client *http.Client) error {
	rawPath, _, err := p.FetchRemoteCached(ctx, remoteURL, client)
	if err != nil {
		return err
	}
	return p.ServeFile(w, r, rawPath, tag, maxWidth, maxHeight, quality)
}

// FetchRemoteCached downloads a remote image into disk cache if not already present, returning path and bytes (if fetched).
func (p *Pipeline) FetchRemoteCached(ctx context.Context, remoteURL string, client *http.Client) (string, []byte, error) {
	if remoteURL == "" {
		return "", nil, os.ErrNotExist
	}
	sum := sha256.Sum256([]byte(remoteURL))
	rawKey := hex.EncodeToString(sum[:16])
	rawPath := filepath.Join(p.cacheDir, "remote", rawKey[:2], rawKey+".raw")

	if _, err := os.Stat(rawPath); err == nil {
		return rawPath, nil, nil
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	var fetched []byte
	err := p.doSingleflight("remote:"+rawKey, func() error {
		if _, err := os.Stat(rawPath); err == nil {
			return nil
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, remoteURL, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("remote image HTTP %d", resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 15<<20))
		if err != nil {
			return err
		}
		if err := p.writeCacheFile(rawPath, data); err != nil {
			return err
		}
		fetched = data
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	return rawPath, fetched, nil
}

// Stats returns cached image count and disk usage (memoized for 30s to avoid slow directory walks on dashboard).
func (p *Pipeline) Stats() CacheStats {
	now := time.Now()
	p.statsMu.RLock()
	if now.Before(p.statsExpires) {
		s := p.cachedStats
		p.statsMu.RUnlock()
		return s
	}
	p.statsMu.RUnlock()

	stats := CacheStats{CacheDir: p.cacheDir}
	_ = filepath.WalkDir(p.cacheDir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, statErr := d.Info(); statErr == nil {
			stats.FileCount++
			stats.TotalBytes += fi.Size()
		}
		return nil
	})

	p.statsMu.Lock()
	p.cachedStats = stats
	p.statsExpires = time.Now().Add(30 * time.Second)
	p.statsMu.Unlock()
	return stats
}

// Clear removes all cached resized renditions (never touches original media files).
func (p *Pipeline) Clear() (int64, error) {
	p.statsMu.Lock()
	p.statsExpires = time.Time{}
	p.statsMu.Unlock()

	before := p.Stats()
	if err := os.RemoveAll(p.cacheDir); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(p.cacheDir, 0o755); err != nil {
		return 0, err
	}
	p.statsMu.Lock()
	p.cachedStats = CacheStats{CacheDir: p.cacheDir}
	p.statsExpires = time.Now().Add(30 * time.Second)
	p.statsMu.Unlock()
	return before.FileCount, nil
}

func (p *Pipeline) cachePath(key string) string {
	return filepath.Join(p.cacheDir, key[:2], key+".img")
}

func (p *Pipeline) writeCacheFile(target string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	tmp := target + "." + hex.EncodeToString(rnd[:]) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	p.statsMu.Lock()
	if !p.statsExpires.IsZero() {
		p.cachedStats.FileCount++
		p.cachedStats.TotalBytes += int64(len(data))
	}
	p.statsMu.Unlock()
	return nil
}

func serveWithETag(w http.ResponseWriter, r *http.Request, path, tag string) error {
	etag := `"` + tag + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return os.ErrNotExist
	}
	w.Header().Set("ETag", etag)
	if tag != "" {
		w.Header().Set("Cache-Control", "private, max-age=604800, immutable")
	} else {
		w.Header().Set("Cache-Control", "private, max-age=86400")
	}
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
	return nil
}

func cacheKey(tag string, maxW, maxH, quality int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%dx%d|q%d", tag, maxW, maxH, quality)))
	return hex.EncodeToString(sum[:16])
}

func fitDimensions(srcW, srcH, maxW, maxH int) (int, int) {
	if srcW <= 0 || srcH <= 0 {
		return srcW, srcH
	}
	scale := 1.0
	if maxW > 0 && float64(maxW)/float64(srcW) < scale {
		scale = float64(maxW) / float64(srcW)
	}
	if maxH > 0 && float64(maxH)/float64(srcH) < scale {
		scale = float64(maxH) / float64(srcH)
	}
	dstW := int(float64(srcW) * scale)
	dstH := int(float64(srcH) * scale)
	if dstW < 1 {
		dstW = 1
	}
	if dstH < 1 {
		dstH = 1
	}
	return dstW, dstH
}

func hasAlpha(img stdimg.Image) bool {
	type opaqueCheck interface {
		Opaque() bool
	}
	if oc, ok := img.(opaqueCheck); ok {
		return !oc.Opaque()
	}
	return false
}
