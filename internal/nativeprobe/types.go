package nativeprobe

import "strings"

// StreamInfo represents a probed video, audio, or embedded subtitle stream.
type StreamInfo struct {
	Type        string `json:"Type"` // Video | Audio | Subtitle
	Index       int    `json:"Index"`
	Codec       string `json:"Codec"`
	Language    string `json:"Language,omitempty"`
	Title       string `json:"Title,omitempty"`
	Width       int    `json:"Width,omitempty"`
	Height      int    `json:"Height,omitempty"`
	Channels    int    `json:"Channels,omitempty"`
	VideoRange  string `json:"VideoRange,omitempty"` // SDR | HDR | HDR10 | HLG | DOVI
	IsDefault   bool   `json:"IsDefault"`
	IsForced    bool   `json:"IsForced"`
	IsExternal  bool   `json:"IsExternal"`
}

// Result holds normalized container & stream metadata extracted by nativeprobe or ffprobe.
type Result struct {
	Container    string       `json:"container"`
	DurationSecs float64      `json:"duration_secs"`
	Bitrate      int64        `json:"bitrate"`
	Width        int          `json:"width"`
	Height       int          `json:"height"`
	VideoCodec   string       `json:"video_codec"`
	HDRType      string       `json:"hdr_type"`
	Streams      []StreamInfo `json:"streams"`
	Engine       string       `json:"engine"` // native-mp4 | native-mkv | ffprobe
}

func (r *Result) Finalize(fileSize int64) {
	for i := range r.Streams {
		r.Streams[i].Index = i
		r.Streams[i].IsExternal = false
		if r.Streams[i].Type == "Video" && r.VideoCodec == "" {
			r.VideoCodec = r.Streams[i].Codec
			r.Width = r.Streams[i].Width
			r.Height = r.Streams[i].Height
			if r.HDRType == "" && r.Streams[i].VideoRange != "" && r.Streams[i].VideoRange != "SDR" {
				r.HDRType = r.Streams[i].VideoRange
			}
		}
	}
	if r.Bitrate == 0 && fileSize > 0 && r.DurationSecs > 0 {
		r.Bitrate = int64(float64(fileSize*8) / r.DurationSecs)
	}
}

func normalizeCodec(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case strings.Contains(s, "avc"), strings.Contains(s, "h264"), strings.Contains(s, "v_mpeg4/iso/avc"):
		return "h264"
	case strings.Contains(s, "hvc"), strings.Contains(s, "hev"), strings.Contains(s, "h265"), strings.Contains(s, "v_mpegh/iso/hevc"):
		return "hevc"
	case strings.Contains(s, "av01"), strings.Contains(s, "v_av1"):
		return "av1"
	case strings.Contains(s, "vp09"), strings.Contains(s, "v_vp9"):
		return "vp9"
	case strings.Contains(s, "vp08"), strings.Contains(s, "v_vp8"):
		return "vp8"
	case strings.Contains(s, "mp4a"), strings.Contains(s, "a_aac"):
		return "aac"
	case strings.Contains(s, "ec-3"), strings.Contains(s, "a_eac3"):
		return "eac3"
	case strings.Contains(s, "ac-3"), strings.Contains(s, "a_ac3"):
		return "ac3"
	case strings.Contains(s, "a_truehd"), strings.Contains(s, "mlpa"):
		return "truehd"
	case strings.Contains(s, "a_dts"), strings.Contains(s, "dtsc"), strings.Contains(s, "dtsh"):
		return "dts"
	case strings.Contains(s, "flac"), strings.Contains(s, "a_flac"):
		return "flac"
	case strings.Contains(s, "opus"), strings.Contains(s, "a_opus"):
		return "opus"
	case strings.Contains(s, "a_mpeg/l3"), strings.Contains(s, "mp3"):
		return "mp3"
	case strings.Contains(s, "s_text/utf8"), strings.Contains(s, "subrip"), strings.Contains(s, "tx3g"):
		return "srt"
	case strings.Contains(s, "s_text/ass"):
		return "ass"
	case strings.Contains(s, "s_text/ssa"):
		return "ssa"
	case strings.Contains(s, "s_hdmv/pgs"):
		return "pgssub"
	case strings.Contains(s, "s_vobsub"):
		return "dvdsub"
	case strings.Contains(s, "wvtt"), strings.Contains(s, "webvtt"):
		return "vtt"
	default:
		return s
	}
}
