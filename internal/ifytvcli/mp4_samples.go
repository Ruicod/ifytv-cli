package ifytvcli

// Fixed-width FullBox tables start with version/flags and an entry count.
func mp4Table(body []byte, width int) []byte {
	if len(body) < 8 || body[0] != 0 {
		return nil
	}
	count := mp4Order.Uint32(body[4:])
	if uint64(count)*uint64(width) != uint64(len(body)-8) {
		return nil
	}
	return body[8:]
}

type mp4Sizes struct {
	count, fixed uint32
	bits         int
	data         []byte
}

func mp4SampleSizes(tables mp4Boxes) (mp4Sizes, bool) {
	var sizes mp4Sizes
	normal, compact := tables.one("stsz"), tables.one("stz2")
	if (normal == nil) == (compact == nil) {
		return sizes, false
	}
	body := normal
	if body == nil {
		body = compact
	}
	if len(body) < 12 || body[0] != 0 {
		return sizes, false
	}
	sizes.count = mp4Order.Uint32(body[8:])
	sizes.data = body[12:]
	if normal != nil {
		sizes.fixed = mp4Order.Uint32(body[4:])
		if sizes.fixed != 0 {
			return sizes, len(sizes.data) == 0
		}
		sizes.bits = 32
	} else {
		sizes.bits = int(body[7])
		switch sizes.bits {
		case 4, 8, 16:
		default:
			return sizes, false
		}
	}
	bytesNeeded := (uint64(sizes.count)*uint64(sizes.bits) + 7) / 8
	return sizes, bytesNeeded == uint64(len(sizes.data))
}

func (s mp4Sizes) at(i uint32) uint32 {
	switch s.bits {
	case 4:
		value := s.data[i/2]
		if i%2 == 0 {
			value >>= 4
		}
		return uint32(value & 15)
	case 8:
		return uint32(s.data[i])
	case 16:
		return uint32(mp4Order.Uint16(s.data[i*2:]))
	case 32:
		return mp4Order.Uint32(s.data[i*4:])
	default:
		return s.fixed
	}
}

// stsc maps chunk numbers (one-based) to a sample count and description.
// Resolve all chunks, including the last one, and reconcile both independent
// counts (stts and stsz/stz2) with the samples actually found.
func (f *mp4File) chunks(tables mp4Boxes, descriptions []mp4Description) bool {
	sizes, ok := mp4SampleSizes(tables)
	timing := mp4Table(tables.one("stts"), 8)
	mapping := mp4Table(tables.one("stsc"), 12)
	if !ok || timing == nil || mapping == nil || uint64(sizes.count) > uint64(f.workLeft) {
		return false
	}
	var timed uint64
	for len(timing) != 0 {
		timed += uint64(mp4Order.Uint32(timing))
		timing = timing[8:]
	}
	if timed != uint64(sizes.count) {
		return false
	}
	short, long := tables.one("stco"), tables.one("co64")
	if (short == nil) == (long == nil) {
		return false
	}
	width := 4
	offsets := mp4Table(short, width)
	if long != nil {
		width = 8
		offsets = mp4Table(long, width)
	}
	if offsets == nil {
		return false
	}
	chunks := len(offsets) / width
	var sample uint32
	for len(mapping) != 0 {
		first := mp4Order.Uint32(mapping)
		perChunk := mp4Order.Uint32(mapping[4:])
		index := mp4Order.Uint32(mapping[8:])
		if first == 0 || (sample == 0 && first != 1) || perChunk == 0 || index == 0 || uint64(index) > uint64(len(descriptions)) {
			return false
		}
		mapping = mapping[12:]
		next := uint32(chunks) + 1
		if len(mapping) != 0 {
			next = mp4Order.Uint32(mapping)
		}
		if next <= first || uint64(next) > uint64(chunks)+1 {
			return false
		}
		for chunk := first; chunk < next; chunk++ {
			if perChunk > sizes.count-sample {
				return false
			}
			b := offsets[int(chunk-1)*width:]
			offset := uint64(mp4Order.Uint32(b))
			if width == 8 {
				offset = mp4Order.Uint64(b)
			}
			if offset > uint64(f.size) {
				return false
			}
			pos := int64(offset)
			for stop := sample + perChunk; sample < stop; sample++ {
				n := sizes.at(sample)
				if !f.sample(descriptions[index-1], pos, n) {
					return false
				}
				pos += int64(n)
			}
		}
	}
	return sample == sizes.count && (sample != 0 || chunks == 0)
}
