package nativeprobe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Options struct {
	EnableFFprobeFallback bool
	FFprobePath           string
	HTTPTimeout           time.Duration
}

type Prober struct {
	opts       Options
	httpClient *http.Client
	ffprobeBin string
}

func New(opts Options) *Prober {
	timeout := opts.HTTPTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	p := &Prober{
		opts: opts,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
	if opts.EnableFFprobeFallback {
		bin := opts.FFprobePath
		if bin == "" {
			if found, err := exec.LookPath("ffprobe"); err == nil {
				bin = found
			}
		}
		p.ffprobeBin = bin
	}
	return p
}

func (p *Prober) HasFFprobe() bool {
	return p.ffprobeBin != ""
}

// ProbeLocal probes a local media file using pure-Go parsers first, with optional ffprobe fallback.
func (p *Prober) ProbeLocal(ctx context.Context, path string) (*Result, error) {
	if res, err := p.ProbeLocalNativeOnly(ctx, path); err == nil {
		return res, nil
	}
	if p.ffprobeBin != "" {
		return p.probeWithFFprobe(ctx, path)
	}
	return nil, errors.New("unsupported container for nativeprobe and ffprobe fallback unavailable")
}

// ProbeLocalNativeOnly performs fast pure-Go container header probing without invoking external ffprobe.
func (p *Prober) ProbeLocalNativeOnly(_ context.Context, path string) (*Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}

	ext := strings.ToLower(filepath.Ext(path))
	return probeReadSeeker(f, fi.Size(), ext)
}

// ProbeRemoteURL probes an HTTP/HTTPS STRM target using bounded Range requests (never downloads the whole file).
func (p *Prober) ProbeRemoteURL(ctx context.Context, targetURL, extHint string) (*Result, error) {
	rr, err := NewBoundedHTTPRangeReader(ctx, p.httpClient, targetURL)
	if err != nil {
		return nil, err
	}
	if res, err := probeReadSeeker(rr, rr.Size(), extHint); err == nil {
		return res, nil
	}
	if p.ffprobeBin != "" {
		return p.probeWithFFprobe(ctx, targetURL)
	}
	return nil, errors.New("remote container could not be parsed via nativeprobe")
}

func probeReadSeeker(rs io.ReadSeeker, size int64, extHint string) (*Result, error) {
	// Detect by magic bytes first, regardless of extension.
	var head [12]byte
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	n, _ := io.ReadFull(rs, head[:])
	if n >= 4 && head[0] == 0x1A && head[1] == 0x45 && head[2] == 0xDF && head[3] == 0xA3 {
		return ProbeMKV(rs, size)
	}
	if n >= 8 && string(head[4:8]) == "ftyp" {
		return ProbeMP4(rs, size)
	}

	// Extension hint fallback
	switch strings.ToLower(strings.TrimPrefix(extHint, ".")) {
	case "mp4", "mov", "m4v":
		return ProbeMP4(rs, size)
	case "mkv", "webm":
		return ProbeMKV(rs, size)
	}
	return nil, errors.New("unrecognized container magic")
}

func (p *Prober) probeWithFFprobe(ctx context.Context, target string) (*Result, error) {
	runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(runCtx, p.ffprobeBin,
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		target,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Format struct {
			FormatName string `json:"format_name"`
			Duration   string `json:"duration"`
			BitRate    string `json:"bit_rate"`
			Size       string `json:"size"`
		} `json:"format"`
		Streams []struct {
			Index         int               `json:"index"`
			CodecType     string            `json:"codec_type"`
			CodecName     string            `json:"codec_name"`
			Width         int               `json:"width"`
			Height        int               `json:"height"`
			Channels      int               `json:"channels"`
			ColorTransfer string            `json:"color_transfer"`
			Tags          map[string]string `json:"tags"`
			Disposition   map[string]int    `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, err
	}

	res := &Result{Engine: "ffprobe"}
	if names := strings.Split(parsed.Format.FormatName, ","); len(names) > 0 {
		res.Container = strings.TrimSpace(names[0])
		if res.Container == "matroska" {
			res.Container = "mkv"
		}
	}
	res.DurationSecs, _ = strconv.ParseFloat(parsed.Format.Duration, 64)
	res.Bitrate, _ = strconv.ParseInt(parsed.Format.BitRate, 10, 64)
	fileSize, _ := strconv.ParseInt(parsed.Format.Size, 10, 64)

	for _, s := range parsed.Streams {
		var st StreamInfo
		st.Codec = normalizeCodec(s.CodecName)
		st.Language = s.Tags["language"]
		st.Title = s.Tags["title"]
		st.IsDefault = s.Disposition["default"] == 1
		st.IsForced = s.Disposition["forced"] == 1
		switch s.CodecType {
		case "video":
			st.Type = "Video"
			st.Width = s.Width
			st.Height = s.Height
			st.VideoRange = "SDR"
			if s.ColorTransfer == "smpte2084" {
				st.VideoRange = "HDR10"
			} else if s.ColorTransfer == "arib-std-b67" {
				st.VideoRange = "HLG"
			}
		case "audio":
			st.Type = "Audio"
			st.Channels = s.Channels
		case "subtitle":
			st.Type = "Subtitle"
		default:
			continue
		}
		res.Streams = append(res.Streams, st)
	}
	res.Finalize(fileSize)
	return res, nil
}
