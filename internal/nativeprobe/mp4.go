package nativeprobe

import (
	"encoding/binary"
	"errors"
	"io"
	"strings"
)

const maxMoovSize = 32 << 20 // 32MB safety cap for moov atom

// ProbeMP4 parses an ISO BMFF (MP4/MOV/M4V) container from rs without scanning mdat payloads.
func ProbeMP4(rs io.ReadSeeker, totalSize int64) (*Result, error) {
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	res := &Result{Container: "mp4", Engine: "native-mp4"}
	var offset int64
	foundFtyp := false
	foundMoov := false

	for {
		if totalSize > 0 && offset >= totalSize {
			break
		}
		boxType, headerLen, boxSize, err := readBoxHeader(rs, offset, totalSize)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return nil, err
		}
		if boxSize < headerLen {
			break
		}
		payloadSize := boxSize - headerLen

		switch boxType {
		case "ftyp":
			foundFtyp = true
			if payloadSize >= 4 && payloadSize <= 1024 {
				buf := make([]byte, payloadSize)
				if _, err := io.ReadFull(rs, buf); err == nil {
					brand := strings.TrimSpace(string(buf[:4]))
					if brand == "qt" {
						res.Container = "mov"
					}
				}
			}
		case "moov":
			foundMoov = true
			if payloadSize > maxMoovSize {
				return nil, errors.New("moov box exceeds safety limit")
			}
			buf := make([]byte, payloadSize)
			if _, err := io.ReadFull(rs, buf); err != nil {
				return nil, err
			}
			parseMoov(buf, res)
		}

		if foundFtyp && foundMoov {
			break
		}
		offset += boxSize
		if _, err := rs.Seek(offset, io.SeekStart); err != nil {
			break
		}
	}

	if !foundMoov {
		return nil, errors.New("not a valid mp4/mov container (moov missing)")
	}
	res.Finalize(totalSize)
	return res, nil
}

func readBoxHeader(rs io.ReadSeeker, offset, totalSize int64) (boxType string, headerLen, boxSize int64, err error) {
	var hdr [8]byte
	if _, err = io.ReadFull(rs, hdr[:]); err != nil {
		return "", 0, 0, err
	}
	size32 := binary.BigEndian.Uint32(hdr[0:4])
	boxType = string(hdr[4:8])
	headerLen = 8

	switch size32 {
	case 0:
		if totalSize > offset {
			boxSize = totalSize - offset
		} else {
			boxSize = headerLen
		}
	case 1:
		var ext [8]byte
		if _, err = io.ReadFull(rs, ext[:]); err != nil {
			return "", 0, 0, err
		}
		headerLen = 16
		boxSize = int64(binary.BigEndian.Uint64(ext[:]))
	default:
		boxSize = int64(size32)
	}
	return boxType, headerLen, boxSize, nil
}

func iterSubBoxes(data []byte, fn func(boxType string, payload []byte)) {
	pos := 0
	for pos+8 <= len(data) {
		size := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		btype := string(data[pos+4 : pos+8])
		hdr := 8
		if size == 1 && pos+16 <= len(data) {
			size = int(binary.BigEndian.Uint64(data[pos+8 : pos+16]))
			hdr = 16
		} else if size == 0 {
			size = len(data) - pos
		}
		if size < hdr || pos+size > len(data) {
			break
		}
		fn(btype, data[pos+hdr:pos+size])
		pos += size
	}
}

func parseMoov(data []byte, res *Result) {
	iterSubBoxes(data, func(btype string, payload []byte) {
		switch btype {
		case "mvhd":
			parseMvhd(payload, res)
		case "trak":
			if st, ok := parseTrak(payload); ok {
				res.Streams = append(res.Streams, st)
			}
		}
	})
}

func parseMvhd(data []byte, res *Result) {
	if len(data) < 20 {
		return
	}
	ver := data[0]
	if ver == 0 && len(data) >= 20 {
		timescale := binary.BigEndian.Uint32(data[12:16])
		duration := binary.BigEndian.Uint32(data[16:20])
		if timescale > 0 {
			res.DurationSecs = float64(duration) / float64(timescale)
		}
	} else if ver == 1 && len(data) >= 32 {
		timescale := binary.BigEndian.Uint32(data[20:24])
		duration := binary.BigEndian.Uint64(data[24:32])
		if timescale > 0 {
			res.DurationSecs = float64(duration) / float64(timescale)
		}
	}
}

func parseTrak(data []byte) (StreamInfo, bool) {
	var st StreamInfo
	var handler string
	var tkhdWidth, tkhdHeight int

	iterSubBoxes(data, func(btype string, payload []byte) {
		switch btype {
		case "tkhd":
			w, h := parseTkhdDims(payload)
			if w > 0 && h > 0 {
				tkhdWidth, tkhdHeight = w, h
			}
		case "mdia":
			iterSubBoxes(payload, func(mt string, mp []byte) {
				switch mt {
				case "mdhd":
					if lang := parseMdhdLang(mp); lang != "" && lang != "und" {
						st.Language = lang
					}
				case "hdlr":
					if len(mp) >= 12 {
						handler = string(mp[8:12])
					}
				case "minf":
					iterSubBoxes(mp, func(mft string, mfp []byte) {
						if mft == "stbl" {
							iterSubBoxes(mfp, func(stbt string, stbp []byte) {
								if stbt == "stsd" {
									parseStsd(stbp, &st)
								}
							})
						}
					})
				}
			})
		}
	})

	switch handler {
	case "vide":
		st.Type = "Video"
		if st.Width == 0 && tkhdWidth > 0 {
			st.Width = tkhdWidth
			st.Height = tkhdHeight
		}
		if st.VideoRange == "" {
			st.VideoRange = "SDR"
		}
		return st, true
	case "soun":
		st.Type = "Audio"
		return st, true
	case "sbtl", "text", "subt":
		st.Type = "Subtitle"
		return st, true
	default:
		return st, false
	}
}

func parseTkhdDims(data []byte) (int, int) {
	if len(data) < 84 {
		return 0, 0
	}
	ver := data[0]
	off := 76
	if ver == 1 {
		off = 88
	}
	if len(data) < off+8 {
		return 0, 0
	}
	w := int(binary.BigEndian.Uint32(data[off:off+4]) >> 16)
	h := int(binary.BigEndian.Uint32(data[off+4:off+8]) >> 16)
	return w, h
}

func parseMdhdLang(data []byte) string {
	if len(data) < 22 {
		return ""
	}
	off := 20
	if data[0] == 1 {
		off = 32
	}
	if len(data) < off+2 {
		return ""
	}
	code := binary.BigEndian.Uint16(data[off : off+2])
	c1 := byte(((code >> 10) & 0x1F) + 0x60)
	c2 := byte(((code >> 5) & 0x1F) + 0x60)
	c3 := byte((code & 0x1F) + 0x60)
	if c1 < 'a' || c1 > 'z' || c2 < 'a' || c2 > 'z' || c3 < 'a' || c3 > 'z' {
		return ""
	}
	return string([]byte{c1, c2, c3})
}

func parseStsd(data []byte, st *StreamInfo) {
	if len(data) < 16 {
		return
	}
	// FullBox: 4 bytes version/flags + 4 bytes entry_count
	entryData := data[8:]
	iterSubBoxes(entryData, func(fourCC string, payload []byte) {
		if st.Codec == "" {
			st.Codec = normalizeCodec(fourCC)
		}
		// VisualSampleEntry: width @ offset 24, height @ offset 26 (after 8-byte box header -> payload[16..20]?)
		// In VisualSampleEntry payload (after 8-byte header):
		// 6 reserved + 2 data_ref_idx + 16 pre_defined/reserved = 24 bytes, then 2 width + 2 height.
		if len(payload) >= 78 {
			w := int(binary.BigEndian.Uint16(payload[24:26]))
			h := int(binary.BigEndian.Uint16(payload[26:28]))
			if w >= 16 && w <= 16384 && h >= 16 && h <= 16384 && st.Width == 0 {
				st.Width = w
				st.Height = h
			}
			// Scan nested video child boxes (colr, dvcC, dvvC, mdcv) starting after 78-byte VisualSampleEntry fixed fields.
			iterSubBoxes(payload[78:], func(sub string, sp []byte) {
				switch sub {
				case "dvcC", "dvvC":
					st.VideoRange = "DOVI"
				case "colr":
					if len(sp) >= 10 && string(sp[:4]) == "nclx" {
						transfer := binary.BigEndian.Uint16(sp[6:8])
						if transfer == 16 && st.VideoRange == "" {
							st.VideoRange = "HDR10"
						} else if transfer == 18 && st.VideoRange == "" {
							st.VideoRange = "HLG"
						}
					}
				}
			})
		}
		// AudioSampleEntry: after 8-byte header -> 6 reserved + 2 data_ref_idx + 8 reserved = 16 bytes, then 2 channels.
		if len(payload) >= 20 && st.Channels == 0 {
			ch := int(binary.BigEndian.Uint16(payload[16:18]))
			if ch >= 1 && ch <= 16 {
				st.Channels = ch
			}
		}
	})
}
