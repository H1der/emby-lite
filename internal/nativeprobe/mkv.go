package nativeprobe

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"strings"
)

// Matroska / EBML Element IDs
const (
	idEBML           = 0x1A45DFA3
	idDocType        = 0x4282
	idSegment        = 0x18538067
	idInfo           = 0x1549A966
	idTimestampScale = 0x2AD7B1
	idDuration       = 0x4489
	idTracks         = 0x1654AE6B
	idTrackEntry     = 0xAE
	idTrackType      = 0x83
	idFlagDefault    = 0x88
	idFlagForced     = 0x55AA
	idName           = 0x536E
	idLanguage       = 0x22B59C
	idLanguageBCP47  = 0x22B59D
	idCodecID        = 0x86
	idVideo          = 0xE0
	idPixelWidth     = 0xB0
	idPixelHeight    = 0xBA
	idColour         = 0x55B0
	idTransferChar   = 0x55BA
	idAudio          = 0xE1
	idChannels       = 0x9F
	idCluster        = 0x1F43B675
)

const maxMKVScanBytes = 8 << 20 // stop scanning if headers aren't in first 8MB

// ProbeMKV parses an EBML (MKV/WebM) header and extracts Info + Tracks without reading Clusters.
func ProbeMKV(rs io.ReadSeeker, totalSize int64) (*Result, error) {
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	var magic [4]byte
	if _, err := io.ReadFull(rs, magic[:]); err != nil {
		return nil, err
	}
	if binary.BigEndian.Uint32(magic[:]) != idEBML {
		return nil, errors.New("not an EBML/MKV container")
	}
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	res := &Result{Container: "mkv", Engine: "native-mkv"}
	gotInfo := false
	gotTracks := false

	// Read EBML header element
	id, size, _, err := readEBMLElementHeader(rs)
	if err != nil || id != idEBML {
		return nil, errors.New("invalid EBML header")
	}
	if size > 0 && size < 1<<20 {
		ebmlData := make([]byte, size)
		if _, err := io.ReadFull(rs, ebmlData); err == nil {
			iterEBML(ebmlData, func(cid uint64, cdata []byte) {
				if cid == idDocType && strings.EqualFold(string(cdata), "webm") {
					res.Container = "webm"
				}
			})
		}
	}

	// Find Segment
	segID, _, _, err := readEBMLElementHeader(rs)
	if err != nil || segID != idSegment {
		return nil, errors.New("MKV Segment element not found")
	}

	var scanned int64
	for scanned < maxMKVScanBytes {
		eid, esize, hdrBytes, err := readEBMLElementHeader(rs)
		if err != nil {
			break
		}
		scanned += int64(hdrBytes)

		// Stop as soon as we reach Cluster (video frames) if we already have Info or Tracks,
		// or avoid reading gigantic media clusters.
		if eid == idCluster {
			break
		}

		switch eid {
		case idInfo:
			if esize > 0 && esize <= 2<<20 {
				buf := make([]byte, esize)
				if _, err := io.ReadFull(rs, buf); err == nil {
					parseMKVInfo(buf, res)
					gotInfo = true
				}
				scanned += esize
				continue
			}
		case idTracks:
			if esize > 0 && esize <= 4<<20 {
				buf := make([]byte, esize)
				if _, err := io.ReadFull(rs, buf); err == nil {
					parseMKVTracks(buf, res)
					gotTracks = true
				}
				scanned += esize
				continue
			}
		}

		if gotInfo && gotTracks {
			break
		}

		// Skip unknown / SeekHead / Cues / Tags / Attachments
		if esize < 0 || esize > 64<<20 {
			break
		}
		if _, err := rs.Seek(esize, io.SeekCurrent); err != nil {
			break
		}
		scanned += esize
	}

	if !gotInfo && !gotTracks {
		return nil, errors.New("MKV Info and Tracks not found")
	}
	res.Finalize(totalSize)
	return res, nil
}

func parseMKVInfo(data []byte, res *Result) {
	var scale uint64 = 1000000 // default 1ms in nanoseconds
	var dur float64
	iterEBML(data, func(id uint64, val []byte) {
		switch id {
		case idTimestampScale:
			if v := readUint(val); v > 0 {
				scale = v
			}
		case idDuration:
			dur = readFloat(val)
		}
	})
	if dur > 0 {
		res.DurationSecs = (dur * float64(scale)) / 1e9
	}
}

func parseMKVTracks(data []byte, res *Result) {
	iterEBML(data, func(id uint64, val []byte) {
		if id != idTrackEntry {
			return
		}
		var st StreamInfo
		var trackType uint64
		iterEBML(val, func(tid uint64, tval []byte) {
			switch tid {
			case idTrackType:
				trackType = readUint(tval)
			case idCodecID:
				st.Codec = normalizeCodec(string(tval))
			case idLanguage:
				if st.Language == "" {
					st.Language = strings.TrimSpace(string(tval))
				}
			case idLanguageBCP47:
				st.Language = strings.TrimSpace(string(tval))
			case idName:
				st.Title = strings.TrimSpace(string(tval))
			case idFlagDefault:
				st.IsDefault = readUint(tval) != 0
			case idFlagForced:
				st.IsForced = readUint(tval) != 0
			case idVideo:
				iterEBML(tval, func(vid uint64, vval []byte) {
					switch vid {
					case idPixelWidth:
						st.Width = int(readUint(vval))
					case idPixelHeight:
						st.Height = int(readUint(vval))
					case idColour:
						iterEBML(vval, func(cid uint64, cval []byte) {
							if cid == idTransferChar {
								tr := readUint(cval)
								if tr == 16 {
									st.VideoRange = "HDR10"
								} else if tr == 18 {
									st.VideoRange = "HLG"
								}
							}
						})
					}
				})
			case idAudio:
				iterEBML(tval, func(aid uint64, aval []byte) {
					if aid == idChannels {
						st.Channels = int(readUint(aval))
					}
				})
			}
		})
		switch trackType {
		case 1:
			st.Type = "Video"
			if st.VideoRange == "" {
				st.VideoRange = "SDR"
			}
			res.Streams = append(res.Streams, st)
		case 2:
			st.Type = "Audio"
			res.Streams = append(res.Streams, st)
		case 17:
			st.Type = "Subtitle"
			res.Streams = append(res.Streams, st)
		}
	})
}

func readEBMLElementHeader(r io.Reader) (id uint64, size int64, consumed int, err error) {
	id, n1, err := readVINT(r, false)
	if err != nil {
		return 0, 0, 0, err
	}
	uSize, n2, err := readVINT(r, true)
	if err != nil {
		return 0, 0, n1, err
	}
	// Check unknown-size sentinel (all 1 bits after marker)
	allOnes := (uint64(1) << (7 * n2)) - 1
	if uSize == allOnes {
		return id, -1, n1 + n2, nil
	}
	return id, int64(uSize), n1 + n2, nil
}

func readVINT(r io.Reader, stripMarker bool) (uint64, int, error) {
	var first [1]byte
	if _, err := io.ReadFull(r, first[:]); err != nil {
		return 0, 0, err
	}
	b := first[0]
	if b == 0 {
		return 0, 1, errors.New("invalid VINT leading zero")
	}
	length := 1
	mask := byte(0x80)
	for (b & mask) == 0 {
		mask >>= 1
		length++
		if length > 8 {
			return 0, 1, errors.New("VINT exceeds 8 bytes")
		}
	}
	var val uint64
	if stripMarker {
		val = uint64(b &^ mask)
	} else {
		val = uint64(b)
	}
	if length > 1 {
		rest := make([]byte, length-1)
		if _, err := io.ReadFull(r, rest); err != nil {
			return 0, 1, err
		}
		for _, rb := range rest {
			val = (val << 8) | uint64(rb)
		}
	}
	return val, length, nil
}

func iterEBML(data []byte, fn func(id uint64, val []byte)) {
	pos := 0
	for pos < len(data) {
		id, n1 := parseVINTBytes(data[pos:], false)
		if n1 <= 0 || pos+n1 >= len(data) {
			break
		}
		pos += n1
		size, n2 := parseVINTBytes(data[pos:], true)
		if n2 <= 0 {
			break
		}
		pos += n2
		end := pos + int(size)
		if end > len(data) || end < pos {
			break
		}
		fn(id, data[pos:end])
		pos = end
	}
}

func parseVINTBytes(buf []byte, stripMarker bool) (uint64, int) {
	if len(buf) == 0 || buf[0] == 0 {
		return 0, 0
	}
	b := buf[0]
	length := 1
	mask := byte(0x80)
	for (b & mask) == 0 {
		mask >>= 1
		length++
		if length > 8 || length > len(buf) {
			return 0, 0
		}
	}
	var val uint64
	if stripMarker {
		val = uint64(b &^ mask)
	} else {
		val = uint64(b)
	}
	for i := 1; i < length; i++ {
		val = (val << 8) | uint64(buf[i])
	}
	return val, length
}

func readUint(b []byte) uint64 {
	var v uint64
	for _, x := range b {
		v = (v << 8) | uint64(x)
	}
	return v
}

func readFloat(b []byte) float64 {
	switch len(b) {
	case 4:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b)))
	case 8:
		return math.Float64frombits(binary.BigEndian.Uint64(b))
	default:
		return 0
	}
}
