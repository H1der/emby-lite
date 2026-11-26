package nativeprobe

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func buildBox(boxType string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(len(out)))
	copy(out[4:8], boxType)
	copy(out[8:], payload)
	return out
}

func buildSyntheticMP4() []byte {
	ftyp := buildBox("ftyp", []byte("isom\x00\x00\x02\x00isommp41"))

	// mvhd v0: timescale=1000, duration=120000 (120s)
	mvhdPayload := make([]byte, 20)
	binary.BigEndian.PutUint32(mvhdPayload[12:16], 1000)
	binary.BigEndian.PutUint32(mvhdPayload[16:20], 120000)
	mvhd := buildBox("mvhd", mvhdPayload)

	// Video trak: hdlr=vide, stsd=hvc1 3840x2160 + colr nclx HDR10 (transfer=16)
	hdlrPayload := make([]byte, 12)
	copy(hdlrPayload[8:12], "vide")
	colr := buildBox("colr", []byte{'n', 'c', 'l', 'x', 0, 9, 0, 16, 0, 9, 0})
	visEntry := make([]byte, 78+len(colr))
	binary.BigEndian.PutUint16(visEntry[24:26], 3840)
	binary.BigEndian.PutUint16(visEntry[26:28], 2160)
	copy(visEntry[78:], colr)
	hvc1 := buildBox("hvc1", visEntry)
	stsdPayload := append([]byte{0, 0, 0, 0, 0, 0, 0, 1}, hvc1...)
	stbl := buildBox("stbl", buildBox("stsd", stsdPayload))
	minf := buildBox("minf", stbl)
	mdia := buildBox("mdia", append(buildBox("hdlr", hdlrPayload), minf...))
	videTrak := buildBox("trak", mdia)

	// Audio trak: hdlr=soun, stsd=mp4a 6 channels
	ahdlr := make([]byte, 12)
	copy(ahdlr[8:12], "soun")
	audEntry := make([]byte, 28)
	binary.BigEndian.PutUint16(audEntry[16:18], 6)
	mp4a := buildBox("mp4a", audEntry)
	astbl := buildBox("stbl", buildBox("stsd", append([]byte{0, 0, 0, 0, 0, 0, 0, 1}, mp4a...)))
	amdia := buildBox("mdia", append(buildBox("hdlr", ahdlr), buildBox("minf", astbl)...))
	audTrak := buildBox("trak", amdia)

	moov := buildBox("moov", append(mvhd, append(videTrak, audTrak...)...))
	mdat := buildBox("mdat", make([]byte, 4096))
	return append(ftyp, append(moov, mdat...)...)
}

func encodeEBML(id uint32, data []byte) []byte {
	var idBytes []byte
	switch {
	case id <= 0xFF:
		idBytes = []byte{byte(id)}
	case id <= 0xFFFF:
		idBytes = []byte{byte(id >> 8), byte(id)}
	case id <= 0xFFFFFF:
		idBytes = []byte{byte(id >> 16), byte(id >> 8), byte(id)}
	default:
		idBytes = []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)}
	}
	l := len(data)
	var sizeBytes []byte
	if l < 127 {
		sizeBytes = []byte{byte(0x80 | l)}
	} else {
		sizeBytes = []byte{byte(0x40 | (l >> 8)), byte(l & 0xFF)}
	}
	out := append(idBytes, sizeBytes...)
	return append(out, data...)
}

func buildSyntheticMKV() []byte {
	ebml := encodeEBML(idEBML, encodeEBML(idDocType, []byte("matroska")))

	var durBuf [8]byte
	binary.BigEndian.PutUint64(durBuf[:], math.Float64bits(95000.0)) // 95s at 1ms scale
	info := encodeEBML(idInfo, append(
		encodeEBML(idTimestampScale, []byte{0x0F, 0x42, 0x40}), // 1,000,000 ns
		encodeEBML(idDuration, durBuf[:])...,
	))

	vidInner := append(encodeEBML(idPixelWidth, []byte{0x07, 0x80}), encodeEBML(idPixelHeight, []byte{0x04, 0x38})...) // 1920x1080
	vidTrack := encodeEBML(idTrackEntry, bytes.Join([][]byte{
		encodeEBML(idTrackType, []byte{1}),
		encodeEBML(idCodecID, []byte("V_MPEG4/ISO/AVC")),
		encodeEBML(idVideo, vidInner),
	}, nil))
	audTrack := encodeEBML(idTrackEntry, bytes.Join([][]byte{
		encodeEBML(idTrackType, []byte{2}),
		encodeEBML(idCodecID, []byte("A_AC3")),
		encodeEBML(idLanguage, []byte("chi")),
		encodeEBML(idAudio, encodeEBML(idChannels, []byte{6})),
	}, nil))
	tracks := encodeEBML(idTracks, append(vidTrack, audTrack...))
	cluster := encodeEBML(idCluster, make([]byte, 64))
	seg := encodeEBML(idSegment, append(info, append(tracks, cluster...)...))
	return append(ebml, seg...)
}

func TestProbeMP4AndHTTPRange(t *testing.T) {
	mp4Bytes := buildSyntheticMP4()

	// 1. Direct ReadSeeker probe
	res, err := ProbeMP4(bytes.NewReader(mp4Bytes), int64(len(mp4Bytes)))
	if err != nil {
		t.Fatalf("ProbeMP4 failed: %v", err)
	}
	if res.VideoCodec != "hevc" || res.Width != 3840 || res.Height != 2160 || res.HDRType != "HDR10" {
		t.Fatalf("unexpected MP4 video result: %+v", res)
	}
	if res.DurationSecs < 119.9 || res.DurationSecs > 120.1 {
		t.Fatalf("unexpected duration: %v", res.DurationSecs)
	}
	if len(res.Streams) != 2 || res.Streams[1].Codec != "aac" || res.Streams[1].Channels != 6 {
		t.Fatalf("unexpected streams: %+v", res.Streams)
	}

	// 2. Bounded HTTP Range probe (STRM simulation)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "movie.mp4", time.Time{}, bytes.NewReader(mp4Bytes))
	}))
	defer ts.Close()

	prober := New(Options{EnableFFprobeFallback: false})
	remoteRes, err := prober.ProbeRemoteURL(context.Background(), ts.URL+"/movie.mp4", "mp4")
	if err != nil {
		t.Fatalf("ProbeRemoteURL failed: %v", err)
	}
	if remoteRes.VideoCodec != "hevc" || remoteRes.Width != 3840 {
		t.Fatalf("unexpected remote result: %+v", remoteRes)
	}
}

func TestProbeMKV(t *testing.T) {
	mkvBytes := buildSyntheticMKV()
	res, err := ProbeMKV(bytes.NewReader(mkvBytes), int64(len(mkvBytes)))
	if err != nil {
		t.Fatalf("ProbeMKV failed: %v", err)
	}
	if res.Container != "mkv" || res.VideoCodec != "h264" || res.Width != 1920 || res.Height != 1080 {
		t.Fatalf("unexpected MKV video result: %+v", res)
	}
	if res.DurationSecs < 94.9 || res.DurationSecs > 95.1 {
		t.Fatalf("unexpected MKV duration: %v", res.DurationSecs)
	}
	if len(res.Streams) != 2 || res.Streams[1].Codec != "ac3" || res.Streams[1].Language != "chi" || res.Streams[1].Channels != 6 {
		t.Fatalf("unexpected MKV audio stream: %+v", res.Streams)
	}

	// Emby/Yamby strict serializers require explicit IsDefault/IsForced booleans even when false.
	raw, err := json.Marshal(res.Streams)
	if err != nil {
		t.Fatalf("marshal streams: %v", err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal streams: %v", err)
	}
	for i, st := range decoded {
		if _, ok := st["IsDefault"].(bool); !ok {
			t.Fatalf("stream[%d] missing bool IsDefault: %s", i, string(raw))
		}
		if _, ok := st["IsForced"].(bool); !ok {
			t.Fatalf("stream[%d] missing bool IsForced: %s", i, string(raw))
		}
		if _, ok := st["IsExternal"].(bool); !ok {
			t.Fatalf("stream[%d] missing bool IsExternal: %s", i, string(raw))
		}
	}
}
