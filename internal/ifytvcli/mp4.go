package ifytvcli

import (
	"context"
	"encoding/binary"
	"io"
	"sort"
)

var mp4Order = binary.BigEndian

// Limit metadata allocations and work independently of the size of the mdat.
const (
	maxMP4Metadata        = 64 << 20
	maxMP4Boxes           = 1 << 16
	maxMP4ValidationSteps = 8 << 20 // one per sample or AVC/HEVC unit
)

func mp4StartBox(kind string) bool {
	switch kind {
	case "ftyp", "free", "skip", "wide":
		return true
	}
	return false
}

// A zero size extends to the end of the enclosing box (or file). Compare
// unsigned lengths before converting them to offsets or using them in slices.
func mp4BoxSize(b []byte, remaining int64) (header, size int64) {
	if len(b) < 8 {
		return 0, 0
	}
	header = 8
	n := uint64(mp4Order.Uint32(b))
	switch n {
	case 0:
		n = uint64(remaining)
	case 1:
		if len(b) < 16 {
			return 0, 0
		}
		header, n = 16, mp4Order.Uint64(b[8:])
	}
	if string(b[4:8]) == "uuid" {
		header += 16
	}
	if n < uint64(header) || n > uint64(remaining) {
		return 0, 0
	}
	return header, int64(n)
}

type mp4Box struct {
	kind string
	body []byte
}

type mp4Boxes []mp4Box

// Only descend into known container types. Unknown extensions are opaque.
func mp4Children(b []byte) mp4Boxes {
	if b == nil {
		return nil
	}
	boxes := mp4Boxes{}
	for len(b) > 0 {
		header, size := mp4BoxSize(b, int64(len(b)))
		if size == 0 || len(boxes) >= maxMP4Boxes {
			return nil
		}
		boxes = append(boxes, mp4Box{string(b[4:8]), b[header:size]})
		b = b[size:]
	}
	return boxes
}

func (boxes mp4Boxes) one(kind string) []byte {
	var body []byte
	for _, box := range boxes {
		if box.kind == kind {
			if body != nil {
				return nil
			}
			body = box.body
		}
	}
	return body
}

type mp4Location struct {
	start, payload, end int64
}

type mp4Description struct {
	video, hevc bool
	local       bool
	nalLength   int
}

type mp4Track struct {
	descriptions []mp4Description
	defaults     *mp4FragmentDefaults
}

type mp4FragmentDefaults struct {
	description, size uint32
}

type mp4File struct {
	ctx       context.Context
	reader    io.ReaderAt
	size      int64
	media     []mp4Location // top-level mdat payloads, in file order
	tracks    map[uint32]*mp4Track
	hasVideo  bool
	workLeft  int
	nalHeader [6]byte
}

func (f *mp4File) metadata(box mp4Location) mp4Boxes {
	n := box.end - box.payload
	if n == 0 || n > maxMP4Metadata {
		return nil
	}
	body := make([]byte, n)
	if _, err := f.reader.ReadAt(body, box.payload); err != nil {
		return nil
	}
	return mp4Children(body)
}

// Index top-level boxes before following offsets: a regular movie can put moov
// on either side of mdat, and fragmented movies can have multiple data extents.
func validMP4Video(ctx context.Context, reader io.ReaderAt, size int64) bool {
	f := mp4File{ctx: ctx, reader: reader, size: size, tracks: make(map[uint32]*mp4Track), workLeft: maxMP4ValidationSteps}
	var movie mp4Location
	var fragments []mp4Location
	var header [16]byte
	var fileType bool
	for pos, count := int64(0), 0; pos < size; count++ {
		if count >= maxMP4Boxes || ctx.Err() != nil {
			return false
		}
		b := header[:min(int64(len(header)), size-pos)]
		if _, err := reader.ReadAt(b, pos); err != nil {
			return false
		}
		h, n := mp4BoxSize(b, size-pos)
		if n == 0 {
			return false
		}
		kind := string(b[4:8])
		if !fileType && !mp4StartBox(kind) {
			return false
		}
		box := mp4Location{pos, pos + h, pos + n}
		switch kind {
		case "ftyp":
			if fileType || n-h < 8 || (n-h)%4 != 0 {
				return false
			}
			fileType = true
		case "moov":
			if movie.end != 0 {
				return false
			}
			movie = box
		case "moof":
			fragments = append(fragments, box)
		case "mdat":
			if box.payload < box.end {
				f.media = append(f.media, box)
			}
		}
		pos = box.end
	}
	if !fileType || len(f.media) == 0 || !f.movie(f.metadata(movie)) {
		return false
	}
	for _, box := range fragments {
		if ctx.Err() != nil || box.start < movie.end || !f.fragment(box.start, f.metadata(box)) {
			return false
		}
	}
	return f.hasVideo
}

// mvhd/mdhd timescale and tkhd track ID follow two version-sized timestamps.
func mp4HeaderWord(b []byte, minSize int) uint32 {
	if len(b) < minSize {
		return 0
	}
	switch b[0] {
	case 0:
		return mp4Order.Uint32(b[12:])
	case 1:
		if len(b) >= minSize+12 {
			return mp4Order.Uint32(b[20:])
		}
	}
	return 0
}

func (f *mp4File) movie(boxes mp4Boxes) bool {
	if mp4HeaderWord(boxes.one("mvhd"), 100) == 0 {
		return false
	}
	for _, box := range boxes {
		if box.kind != "trak" {
			continue
		}
		track := mp4Children(box.body)
		id := mp4HeaderWord(track.one("tkhd"), 84)
		media := mp4Children(track.one("mdia"))
		handler := media.one("hdlr")
		if id == 0 || f.tracks[id] != nil || mp4HeaderWord(media.one("mdhd"), 24) == 0 || len(handler) < 24 {
			return false
		}
		info := mp4Children(media.one("minf"))
		tables := mp4Children(info.one("stbl"))
		dinf := mp4Children(info.one("dinf"))
		desc := mp4Descriptions(tables.one("stsd"), mp4Entries(dinf.one("dref")), string(handler[8:12]) == "vide")
		if len(desc) == 0 || !f.chunks(tables, desc) {
			return false
		}
		f.tracks[id] = &mp4Track{descriptions: desc}
	}
	if ext := boxes.one("mvex"); ext != nil {
		defaults := mp4Children(ext)
		if defaults == nil {
			return false
		}
		for _, box := range defaults {
			if box.kind != "trex" {
				continue
			}
			if len(box.body) != 24 || box.body[0] != 0 {
				return false
			}
			track := f.tracks[mp4Order.Uint32(box.body[4:])]
			if track == nil || track.defaults != nil {
				return false
			}
			track.defaults = &mp4FragmentDefaults{description: mp4Order.Uint32(box.body[8:]), size: mp4Order.Uint32(box.body[16:])}
		}
	}
	return len(f.tracks) > 0
}

func mp4Entries(body []byte) mp4Boxes {
	if len(body) < 8 || body[0] != 0 {
		return nil
	}
	entries := mp4Children(body[8:])
	if len(entries) == 0 || uint32(len(entries)) != mp4Order.Uint32(body[4:]) {
		return nil
	}
	return entries
}

func mp4Descriptions(body []byte, refs mp4Boxes, video bool) []mp4Description {
	entries := mp4Entries(body)
	desc := make([]mp4Description, len(entries))
	for i, entry := range entries {
		b := entry.body
		if len(b) < 8 {
			return nil
		}
		index := int(mp4Order.Uint16(b[6:]))
		if index == 0 || index > len(refs) {
			return nil
		}
		ref := refs[index-1]
		desc[i].local = (ref.kind == "url " || ref.kind == "urn ") && len(ref.body) >= 4 && mp4Order.Uint32(ref.body) == 1
		if !video {
			continue
		}
		// VisualSampleEntry includes a data reference, width, height and a
		// fixed 78-byte header before the codec-specific child boxes.
		if len(b) < 78 || mp4Order.Uint16(b[24:]) == 0 || mp4Order.Uint16(b[26:]) == 0 {
			return nil
		}
		desc[i].video = true
		config := mp4Children(b[78:])
		if config == nil {
			return nil
		}
		switch entry.kind {
		case "avc1", "avc2", "avc3", "avc4":
			avc := config.one("avcC")
			if !mp4AVCConfig(avc) {
				return nil
			}
			desc[i].nalLength = 1 + int(avc[4]&3)
		case "hvc1", "hev1":
			hevc := config.one("hvcC")
			if !mp4HEVCConfig(hevc) {
				return nil
			}
			desc[i].hevc = true
			desc[i].nalLength = 1 + int(hevc[21]&3)
		}
	}
	return desc
}

// Both decoder configurations store parameter sets as 16-bit-length NALs.
func mp4ParameterSets(b []byte, count int) ([]byte, bool) {
	for i := 0; i < count; i++ {
		if len(b) < 2 {
			return nil, false
		}
		n := int(mp4Order.Uint16(b))
		if n == 0 || n > len(b)-2 {
			return nil, false
		}
		b = b[2+n:]
	}
	return b, true
}

func mp4AVCConfig(b []byte) bool {
	if len(b) < 7 || b[0] != 1 {
		return false
	}
	rest, ok := mp4ParameterSets(b[6:], int(b[5]&31)) // sequence parameter sets
	if !ok || len(rest) < 1 {
		return false
	}
	rest, ok = mp4ParameterSets(rest[1:], int(rest[0])) // picture parameter sets
	if !ok || len(rest) == 0 {
		return ok
	}
	// High-profile configuration can also carry SPS extensions.
	if len(rest) < 4 {
		return false
	}
	rest, ok = mp4ParameterSets(rest[4:], int(rest[3]))
	return ok && len(rest) == 0
}

func mp4HEVCConfig(b []byte) bool {
	if len(b) < 23 || b[0] != 1 {
		return false
	}
	rest := b[23:]
	for i := 0; i < int(b[22]); i++ {
		if len(rest) < 3 {
			return false
		}
		var ok bool
		rest, ok = mp4ParameterSets(rest[3:], int(mp4Order.Uint16(rest[1:])))
		if !ok {
			return false
		}
	}
	return len(rest) == 0
}

// Resolve every declared sample into one local mdat. For AVC/HEVC also walk
// the length-prefixed units and require actual coded picture data somewhere.
func (f *mp4File) sample(desc mp4Description, pos int64, size uint32) bool {
	f.workLeft--
	if !desc.local || f.workLeft < 0 || f.ctx.Err() != nil || pos < 0 || pos > f.size {
		return false
	}
	if size == 0 {
		return true
	}
	end := pos + int64(size)
	i := sort.Search(len(f.media), func(i int) bool { return f.media[i].end > pos })
	if i == len(f.media) || pos < f.media[i].payload || end > f.media[i].end {
		return false
	}
	if !desc.video {
		return true
	}
	if desc.nalLength == 0 {
		f.hasVideo = true
		return true
	}
	headerSize := 1
	if desc.hevc {
		headerSize = 2
	}
	for pos < end {
		f.workLeft--
		b := f.nalHeader[:desc.nalLength+headerSize]
		if f.workLeft < 0 || f.ctx.Err() != nil || end-pos < int64(len(b)) {
			return false
		}
		if _, err := f.reader.ReadAt(b, pos); err != nil {
			return false
		}
		var n int64
		for _, octet := range b[:desc.nalLength] {
			n = n<<8 | int64(octet)
		}
		pos += int64(desc.nalLength)
		first := b[desc.nalLength]
		if n < int64(headerSize) || n > end-pos || first&0x80 != 0 {
			return false
		}
		kind := first & 31
		picture := (kind >= 1 && kind <= 5) || (kind >= 19 && kind <= 21)
		if desc.hevc {
			if b[desc.nalLength+1]&7 == 0 {
				return false
			}
			picture = (first>>1)&63 <= 31
		}
		if picture && n > int64(headerSize) {
			f.hasVideo = true
		}
		pos += n
	}
	return true
}
