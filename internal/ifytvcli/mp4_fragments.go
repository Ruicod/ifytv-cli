package ifytvcli

import "math/bits"

type mp4FragmentTrack struct {
	description mp4Description
	defaultSize uint32
	base        int64
	empty       bool
}

func (f *mp4File) fragment(start int64, boxes mp4Boxes) bool {
	if header := boxes.one("mfhd"); len(header) != 8 || header[0] != 0 {
		return false
	}
	hasTrack := false
	// Without an explicit base, the first traf uses the moof start and the
	// next traf inherits the end of the preceding track fragment's data.
	implicitBase := start
	for _, box := range boxes {
		if box.kind != "traf" {
			continue
		}
		children := mp4Children(box.body)
		track, ok := f.fragmentTrack(children.one("tfhd"), start, implicitBase)
		if !ok {
			return false
		}
		end := track.base
		var hasRun bool
		for _, child := range children {
			if child.kind == "trun" {
				end, ok = f.fragmentRun(child.body, track, end)
				if !ok {
					return false
				}
				hasRun = true
			}
		}
		if !hasRun && !track.empty {
			return false
		}
		hasTrack, implicitBase = true, end
	}
	return hasTrack
}

func (f *mp4File) fragmentTrack(b []byte, moof, implicitBase int64) (mp4FragmentTrack, bool) {
	var result mp4FragmentTrack
	if len(b) < 8 || b[0] != 0 {
		return result, false
	}
	flags := mp4Order.Uint32(b)
	track := f.tracks[mp4Order.Uint32(b[4:])]
	if track == nil || track.defaults == nil {
		return result, false
	}
	fields := b[8:]
	result.base = implicitBase
	if flags&0x000001 != 0 { // base-data-offset-present
		if len(fields) < 8 {
			return result, false
		}
		base := mp4Order.Uint64(fields)
		if base > uint64(f.size) {
			return result, false
		}
		result.base, fields = int64(base), fields[8:]
	} else if flags&0x020000 != 0 { // default-base-is-moof
		result.base = moof
	}
	// Optional fields in layout order: description, duration, size, flags.
	if len(fields) != 4*bits.OnesCount32(flags&0x003a) {
		return result, false
	}
	index := track.defaults.description
	if flags&0x000002 != 0 {
		index, fields = mp4Order.Uint32(fields), fields[4:]
	}
	if index == 0 || uint64(index) > uint64(len(track.descriptions)) {
		return result, false
	}
	result.description = track.descriptions[index-1]
	if flags&0x000008 != 0 {
		fields = fields[4:]
	}
	result.defaultSize = track.defaults.size
	if flags&0x000010 != 0 {
		result.defaultSize = mp4Order.Uint32(fields)
	}
	result.empty = flags&0x010000 != 0
	return result, true
}

func (f *mp4File) fragmentRun(b []byte, track mp4FragmentTrack, pos int64) (int64, bool) {
	if len(b) < 8 || b[0] > 1 {
		return 0, false
	}
	flags := mp4Order.Uint32(b) & 0xffffff
	count := mp4Order.Uint32(b[4:])
	fields := b[8:]
	if flags&0x000001 != 0 { // signed offset from the tfhd base
		if len(fields) < 4 {
			return 0, false
		}
		pos = track.base + int64(int32(mp4Order.Uint32(fields)))
		fields = fields[4:]
	}
	if flags&0x000004 != 0 { // first-sample-flags
		if len(fields) < 4 {
			return 0, false
		}
		fields = fields[4:]
	}
	// Per-sample fields in layout order: duration, size, flags, composition offset.
	width := 4 * bits.OnesCount32(flags&0x0f00)
	if uint64(count)*uint64(width) != uint64(len(fields)) || uint64(count) > uint64(f.workLeft) ||
		pos < 0 || pos > f.size || (track.empty && count != 0) {
		return 0, false
	}
	sizeOffset := 0
	if flags&0x000100 != 0 {
		sizeOffset = 4
	}
	for i := uint32(0); i < count; i++ {
		n := track.defaultSize
		if flags&0x000200 != 0 {
			n = mp4Order.Uint32(fields[sizeOffset:])
		}
		if !f.sample(track.description, pos, n) {
			return 0, false
		}
		pos += int64(n)
		fields = fields[width:]
	}
	return pos, true
}
