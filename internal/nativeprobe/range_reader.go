package nativeprobe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const (
	defaultRangeChunkSize = 256 << 10 // 256KB per range fetch
	defaultMaxTotalBudget = 4 << 20   // never fetch more than 4MB from remote STRM
)

// BoundedHTTPRangeReader implements io.ReadSeeker over an HTTP/HTTPS URL using strict Range requests.
type BoundedHTTPRangeReader struct {
	ctx        context.Context
	client     *http.Client
	url        string
	size       int64
	pos        int64
	fetched    int64
	maxBudget  int64
	chunkStart int64
	chunkData  []byte
}

func NewBoundedHTTPRangeReader(ctx context.Context, client *http.Client, targetURL string) (*BoundedHTTPRangeReader, error) {
	if client == nil {
		client = http.DefaultClient
	}
	r := &BoundedHTTPRangeReader{
		ctx:       ctx,
		client:    client,
		url:       targetURL,
		maxBudget: defaultMaxTotalBudget,
	}
	// Fetch initial chunk via Range: bytes=0-<chunk-1> to simultaneously discover total size & buffer header.
	if err := r.fetchChunk(0); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *BoundedHTTPRangeReader) Size() int64 { return r.size }
func (r *BoundedHTTPRangeReader) BytesFetched() int64 { return r.fetched }

func (r *BoundedHTTPRangeReader) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = r.pos + offset
	case io.SeekEnd:
		if r.size <= 0 {
			return 0, errors.New("unknown remote content size")
		}
		next = r.size + offset
	default:
		return 0, errors.New("invalid whence")
	}
	if next < 0 {
		return 0, errors.New("negative seek offset")
	}
	r.pos = next
	return r.pos, nil
}

func (r *BoundedHTTPRangeReader) Read(p []byte) (int, error) {
	if r.size > 0 && r.pos >= r.size {
		return 0, io.EOF
	}
	if r.pos < r.chunkStart || r.pos >= r.chunkStart+int64(len(r.chunkData)) {
		if err := r.fetchChunk(r.pos); err != nil {
			return 0, err
		}
	}
	rel := r.pos - r.chunkStart
	if rel >= int64(len(r.chunkData)) {
		return 0, io.EOF
	}
	n := copy(p, r.chunkData[rel:])
	r.pos += int64(n)
	return n, nil
}

func (r *BoundedHTTPRangeReader) fetchChunk(start int64) error {
	if r.fetched >= r.maxBudget {
		return errors.New("remote probe byte budget exceeded")
	}
	end := start + defaultRangeChunkSize - 1
	if r.size > 0 && end >= r.size {
		end = r.size - 1
	}
	if start > end && r.size > 0 {
		return io.EOF
	}

	req, err := http.NewRequestWithContext(r.ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	req.Header.Set("User-Agent", "Emby-Lite/0.2")

	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("remote server returned HTTP %d", resp.StatusCode)
	}

	// Parse total length from Content-Range: bytes 0-262143/12345678
	if cr := resp.Header.Get("Content-Range"); cr != "" {
		if idx := strings.LastIndex(cr, "/"); idx > 0 {
			if total, parseErr := strconv.ParseInt(strings.TrimSpace(cr[idx+1:]), 10, 64); parseErr == nil && total > 0 {
				r.size = total
			}
		}
	} else if resp.StatusCode == http.StatusOK && resp.ContentLength > 0 && start == 0 {
		r.size = resp.ContentLength
	}

	limit := int64(defaultRangeChunkSize)
	if rem := r.maxBudget - r.fetched; rem < limit {
		limit = rem
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return err
	}
	r.fetched += int64(len(data))
	r.chunkStart = start
	r.chunkData = data
	return nil
}
