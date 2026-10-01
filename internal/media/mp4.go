package media

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
)

// ---------------------------------------------------------------- MP4 index parsing
//
// An MP4 keeps a table of every sample (frame) and where it lies in the file. Reading that table takes a few
// hundred KB; after that any keyframe can be fetched by byte range, so a video on a remote bucket costs about
// 1 to 4 MB to index however long it is.

var be = binary.BigEndian

type box struct {
	typ        string
	start, end int // payload start, box end
}

// boxes lists the boxes found in buf[pos:end].
func boxes(buf []byte, pos, end int) []box {
	var out []box
	for pos+8 <= end {
		size := int(be.Uint32(buf[pos:]))
		typ := string(buf[pos+4 : pos+8])
		hdr := 8
		switch size {
		case 1:
			if pos+16 > end {
				return out
			}
			size, hdr = int(be.Uint64(buf[pos+8:])), 16
		case 0:
			size = end - pos
		}
		if size < hdr {
			return out
		}
		out = append(out, box{typ, pos + hdr, min(pos+size, end)})
		pos += size
	}
	return out
}

func child(buf []byte, start, end int, path ...string) (box, bool) {
	cur := box{start: start, end: end}
	for _, name := range path {
		found := false
		for _, b := range boxes(buf, cur.start, cur.end) {
			if b.typ == name {
				cur, found = b, true
				break
			}
		}
		if !found {
			return box{}, false
		}
	}
	return cur, true
}

// readMoov walks the top-level boxes with small ranged reads and returns the moov box payload.
func readMoov(r Reader, size int64) ([]byte, error) {
	head, err := r.ReadRange(0, min(size, 65536)-1)
	if err != nil {
		return nil, err
	}
	pos := int64(0)
	for pos+8 <= size {
		var hdr []byte
		if pos+16 <= int64(len(head)) {
			hdr = head[pos : pos+16]
		} else if hdr, err = r.ReadRange(pos, min(pos+15, size-1)); err != nil {
			return nil, err
		}
		if len(hdr) < 8 {
			break
		}
		boxSize, typ, hlen := int64(be.Uint32(hdr)), string(hdr[4:8]), int64(8)
		switch boxSize {
		case 1:
			if len(hdr) < 16 {
				return nil, errors.New("corrupt top-level box")
			}
			boxSize, hlen = int64(be.Uint64(hdr[8:])), 16
		case 0:
			boxSize = size - pos
		}
		if boxSize < hlen {
			return nil, errors.New("corrupt top-level box")
		}
		if typ == "moov" {
			end := pos + boxSize
			var b []byte
			if end <= int64(len(head)) {
				b = head[pos:end]
			} else if b, err = r.ReadRange(pos, end-1); err != nil {
				return nil, err
			}
			if int64(len(b)) < hlen {
				return nil, errors.New("truncated moov box")
			}
			return b[hlen:], nil
		}
		pos += boxSize
	}
	return nil, errors.New("no moov box")
}

type track struct {
	handler       string
	timescale     uint32
	duration      uint64
	width, height int
	stbl          box
	hasStbl       bool
}

func parseTracks(moov []byte) []track {
	var out []track
	for _, t := range boxes(moov, 0, len(moov)) {
		if t.typ != "trak" {
			continue
		}
		hd, ok1 := child(moov, t.start, t.end, "mdia", "hdlr")
		md, ok2 := child(moov, t.start, t.end, "mdia", "mdhd")
		tk, ok3 := child(moov, t.start, t.end, "tkhd")
		if !(ok1 && ok2 && ok3) {
			continue
		}
		tr := track{handler: string(moov[hd.start+8 : hd.start+12])}
		if moov[md.start] == 1 {
			tr.timescale, tr.duration = be.Uint32(moov[md.start+20:]), be.Uint64(moov[md.start+24:])
		} else {
			tr.timescale, tr.duration = be.Uint32(moov[md.start+12:]), uint64(be.Uint32(moov[md.start+16:]))
		}
		tr.width, tr.height = int(be.Uint32(moov[tk.end-8:])>>16), int(be.Uint32(moov[tk.end-4:])>>16)
		tr.stbl, tr.hasStbl = child(moov, t.start, t.end, "mdia", "minf", "stbl")
		out = append(out, tr)
	}
	return out
}

type sampleTable struct {
	fourcc        string
	width, height int
	confKind      string
	conf          []byte
	stts          []uint32 // pairs: count, delta
	constSize     uint32
	count         uint32
	sizes         []uint32
	stsc          []uint32 // triples: first chunk, samples per chunk, description
	chunks        []uint64
	sync          []uint32
	hasSync       bool
}

func u32s(buf []byte, pos, count int) []uint32 {
	out := make([]uint32, count)
	for i := range out {
		out[i] = be.Uint32(buf[pos+4*i:])
	}
	return out
}

func readStbl(buf []byte, b box) (*sampleTable, error) {
	need := func(name string) (box, error) {
		x, ok := child(buf, b.start, b.end, name)
		if !ok {
			return box{}, fmt.Errorf("no %s box", name)
		}
		return x, nil
	}
	st := &sampleTable{}
	stsd, err := need("stsd")
	if err != nil {
		return nil, err
	}
	entry := stsd.start + 8
	esize, fourcc := int(be.Uint32(buf[entry:])), string(buf[entry+4:entry+8])
	st.fourcc = fourcc
	st.width, st.height = int(be.Uint16(buf[entry+8+24:])), int(be.Uint16(buf[entry+8+26:]))
	for _, c := range boxes(buf, entry+8+78, min(entry+esize, len(buf))) {
		switch c.typ {
		case "avcC", "hvcC", "av1C":
			st.confKind, st.conf = c.typ, append([]byte(nil), buf[c.start:c.end]...)
		}
	}
	stts, err := need("stts")
	if err != nil {
		return nil, err
	}
	st.stts = u32s(buf, stts.start+8, 2*int(be.Uint32(buf[stts.start+4:])))
	stsz, err := need("stsz")
	if err != nil {
		return nil, err
	}
	st.constSize, st.count = be.Uint32(buf[stsz.start+4:]), be.Uint32(buf[stsz.start+8:])
	if st.constSize == 0 {
		st.sizes = u32s(buf, stsz.start+12, int(st.count))
	}
	stsc, err := need("stsc")
	if err != nil {
		return nil, err
	}
	st.stsc = u32s(buf, stsc.start+8, 3*int(be.Uint32(buf[stsc.start+4:])))
	if co, ok := child(buf, b.start, b.end, "stco"); ok {
		n := int(be.Uint32(buf[co.start+4:]))
		st.chunks = make([]uint64, n)
		for i := range st.chunks {
			st.chunks[i] = uint64(be.Uint32(buf[co.start+8+4*i:]))
		}
	} else if co, ok := child(buf, b.start, b.end, "co64"); ok {
		n := int(be.Uint32(buf[co.start+4:]))
		st.chunks = make([]uint64, n)
		for i := range st.chunks {
			st.chunks[i] = be.Uint64(buf[co.start+8+8*i:])
		}
	} else {
		return nil, errors.New("no chunk offsets")
	}
	if ss, ok := child(buf, b.start, b.end, "stss"); ok {
		st.sync, st.hasSync = u32s(buf, ss.start+8, int(be.Uint32(buf[ss.start+4:]))), true
	}
	return st, nil
}

type syncSample struct {
	number uint32
	time   uint64
}

// syncTimes lists (sample number, decode time) for each sync sample; every ~sample if the track has no stss.
func syncTimes(st *sampleTable) []syncSample {
	sync := st.sync
	if !st.hasSync {
		step := max(1, int(st.count)/400)
		sync = nil
		for n := 1; n <= int(st.count); n += step {
			sync = append(sync, uint32(n))
		}
	}
	var out []syncSample
	i := 0
	sample, t := uint32(1), uint64(0)
	for k := 0; k+1 < len(st.stts); k += 2 {
		cnt, delta := st.stts[k], st.stts[k+1]
		for i < len(sync) && sync[i] < sample+cnt {
			out = append(out, syncSample{sync[i], t + uint64(sync[i]-sample)*uint64(delta)})
			i++
		}
		sample += cnt
		t += uint64(cnt) * uint64(delta)
	}
	return out
}

// sampleLocation is the file offset and size of 1-based sample n.
func sampleLocation(st *sampleTable, n uint32) (off, size int64, err error) {
	if n < 1 || n > st.count {
		return 0, 0, fmt.Errorf("sample %d outside the sample table", n)
	}
	idx, acc := int64(n)-1, int64(0)
	for j := 0; j+2 < len(st.stsc); j += 3 {
		first, perChunk := int64(st.stsc[j]), int64(st.stsc[j+1])
		last := int64(len(st.chunks)) + 1
		if j+3 < len(st.stsc) {
			last = int64(st.stsc[j+3])
		}
		runLen := (last - first) * perChunk
		if idx < acc+runLen {
			chunk := first + (idx-acc)/perChunk
			firstInChunk := acc + (chunk-first)*perChunk
			if chunk < 1 || chunk > int64(len(st.chunks)) {
				break
			}
			base := int64(st.chunks[chunk-1])
			if st.constSize != 0 {
				return base + (idx-firstInChunk)*int64(st.constSize), int64(st.constSize), nil
			}
			if firstInChunk < 0 || idx >= int64(len(st.sizes)) || firstInChunk > idx {
				break
			}
			var skip int64
			for _, s := range st.sizes[firstInChunk:idx] {
				skip += int64(s)
			}
			return base + skip, int64(st.sizes[idx]), nil
		}
		acc += runLen
	}
	return 0, 0, fmt.Errorf("sample %d outside the chunk table", n)
}

// paramSets returns the NAL length size and parameter-set NALs (SPS/PPS, plus VPS for HEVC) from avcC/hvcC.
func paramSets(kind string, c []byte) (nalLen int, params [][]byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("malformed codec configuration")
		}
	}()
	if kind == "avcC" {
		nalLen = int(c[4]&3) + 1
		pos := 6
		groups := []int{int(c[5] & 0x1F)}
		for g := 0; g < 2; g++ {
			for i := 0; i < groups[g]; i++ {
				n := int(be.Uint16(c[pos:]))
				params = append(params, c[pos+2:pos+2+n])
				pos += 2 + n
			}
			if g == 0 {
				groups = append(groups, int(c[pos]))
				pos++
			}
		}
		return nalLen, params, nil
	}
	nalLen = int(c[21]&3) + 1
	pos := 23
	for i := 0; i < int(c[22]); i++ {
		count := int(be.Uint16(c[pos+1:]))
		pos += 3
		for j := 0; j < count; j++ {
			n := int(be.Uint16(c[pos:]))
			params = append(params, c[pos+2:pos+2+n])
			pos += 2 + n
		}
	}
	return nalLen, params, nil
}

// annexB turns a length-prefixed sample into an Annex B stream, parameter sets first.
func annexB(sample []byte, nalLen int, params [][]byte) []byte {
	out := make([]byte, 0, len(sample)+64)
	for _, p := range params {
		out = append(out, 0, 0, 0, 1)
		out = append(out, p...)
	}
	pos := 0
	for pos+nalLen <= len(sample) {
		n := 0
		for _, b := range sample[pos : pos+nalLen] {
			n = n<<8 | int(b)
		}
		pos += nalLen
		if pos+n > len(sample) {
			n = len(sample) - pos
		}
		out = append(out, 0, 0, 0, 1)
		out = append(out, sample[pos:pos+n]...)
		pos += n
	}
	return out
}

// disableBatch makes tests compare one-process decoding against the per-frame fallback.
var disableBatch bool

// batchHits counts files decoded by the one-process path (for tests).
var batchHits atomic.Int64

// frameSem bounds the ffmpeg processes decoding keyframes, shared by every file in progress.
var frameSem = make(chan struct{}, max(8, runtime.NumCPU()*2))

// Meta is what indexing learns about a file.
type Meta struct {
	Duration float64
	Width    int
	Height   int
	Codec    string
	FPS      float64
	Audio    bool
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// mp4Keyframes decodes a few keyframes of an MP4 by reading only their bytes. stem is the thumbnail path without
// its "-<n>.jpg" suffix.
func mp4Keyframes(tools Tools, r Reader, size int64, stem string) (meta Meta, paths []string, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("unreadable MP4 index (%v)", p)
		}
	}()
	moov, err := readMoov(r, size)
	if err != nil {
		return meta, nil, err
	}
	tracks := parseTracks(moov)
	var vt *track
	for i := range tracks {
		if tracks[i].handler == "vide" && tracks[i].hasStbl {
			vt = &tracks[i]
			break
		}
	}
	if vt == nil || vt.duration == 0 {
		return meta, nil, errors.New("no indexed video track")
	}
	st, err := readStbl(moov, vt.stbl)
	if err != nil {
		return meta, nil, err
	}
	if st.conf == nil {
		return meta, nil, fmt.Errorf("no fast path for %s", st.fourcc)
	}
	var format string
	var bitstream func([]byte) []byte
	if st.confKind == "av1C" {
		// AV1 samples are already OBUs. A decodable stream is: temporal delimiter, the sequence header kept in
		// av1C (after its 4 fixed bytes), then the keyframe's own OBUs.
		format = "obu"
		head := append([]byte{0x12, 0x00}, st.conf[4:]...)
		bitstream = func(s []byte) []byte { return append(append([]byte(nil), head...), s...) }
	} else {
		format = "hevc"
		if st.confKind == "avcC" {
			format = "h264"
		}
		nalLen, params, perr := paramSets(st.confKind, st.conf)
		if perr != nil {
			return meta, nil, perr
		}
		bitstream = func(s []byte) []byte { return annexB(s, nalLen, params) }
	}
	duration := float64(vt.duration) / float64(vt.timescale)
	w, h := vt.width, vt.height
	if w == 0 {
		w = st.width
	}
	if h == 0 {
		h = st.height
	}
	audio := false
	for _, t := range tracks {
		audio = audio || t.handler == "soun"
	}
	meta = Meta{Duration: round2(duration), Width: w, Height: h, Codec: st.fourcc, FPS: round2(float64(st.count) / duration), Audio: audio}
	syncs := syncTimes(st)
	if len(syncs) == 0 {
		return meta, nil, errors.New("no sync samples")
	}
	var picks []uint32
	for _, f := range Fractions {
		target := uint64(f * float64(vt.duration))
		i := sort.Search(len(syncs), func(i int) bool { return syncs[i].time > target }) - 1
		n := syncs[max(0, i)].number
		dup := false
		for _, p := range picks {
			dup = dup || p == n
		}
		if !dup {
			picks = append(picks, n)
		}
	}
	// Read the keyframes' bytes side by side (on a remote bucket this is where the time goes) ...
	streams := make([][]byte, len(picks))
	var wg sync.WaitGroup
	for i, n := range picks {
		wg.Add(1)
		go func(i int, n uint32) {
			defer wg.Done()
			defer func() { _ = recover() }() // a malformed table must cost one frame, never the process
			off, length, lerr := sampleLocation(st, n)
			if lerr != nil || length <= 0 {
				return
			}
			if data, rerr := r.ReadRange(off, off+length-1); rerr == nil {
				streams[i] = bitstream(data)
			}
		}(i, n)
	}
	wg.Wait()
	// ... then decode them all in one ffmpeg process, or, if that does not come out right, one process each.
	outs, ok := make([]string, len(picks)), false
	if all(streams) && !disableBatch {
		frameSem <- struct{}{}
		var batch []string
		if batch, ok = tools.decodeBatch(format, streams, stem); ok {
			outs = batch
			batchHits.Add(1)
		}
		<-frameSem
	}
	if !ok {
		for i := range streams {
			if streams[i] == nil {
				continue
			}
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				frameSem <- struct{}{}
				defer func() { <-frameSem }()
				out := fmt.Sprintf("%s-k%d.jpg", stem, i)
				if tools.decodeFrame(format, streams[i], out) {
					outs[i] = out
				}
			}(i)
		}
		wg.Wait()
	}
	paths = numberFrames(stem, outs)
	if len(paths) == 0 {
		return meta, nil, errors.New("no keyframe decoded")
	}
	return meta, paths, nil
}

func all(streams [][]byte) bool {
	for _, s := range streams {
		if s == nil {
			return false
		}
	}
	return true
}

// numberFrames renames the frames that came out (in time order) to <stem>-0.jpg, -1.jpg, ... with no gaps.
func numberFrames(stem string, outs []string) []string {
	var paths []string
	for _, out := range outs {
		if out == "" {
			continue
		}
		final := fmt.Sprintf("%s-%d.jpg", stem, len(paths))
		if err := os.Rename(out, final); err != nil {
			_ = os.Remove(out)
			continue
		}
		paths = append(paths, filepath.ToSlash(final))
	}
	return paths
}
