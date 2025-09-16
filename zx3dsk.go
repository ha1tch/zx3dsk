package main

// zx3dsk_plus3_hdr_spec v2:
// Fixes:
//  - Boot-spec bytes [8]=0x2A (R/W gap), [9]=0x52 (fmt gap) per +3/PCW (CF2) spec.
//  - Directory allocation: CP/M block numbers in directory entries are absolute from
//    the start of the *data area* (after reserved tracks), and start at 0 where block 0..(dirBlocks-1)
//    are the directory area itself. Files must NOT use those; first allocatable block is DirBlocks.
//    We now start allocating at block #2 and write those absolute block numbers into the directory.
//  - CHS mapping now interprets block numbers as absolute (including directory blocks).
// Result: BASIC should fetch the correct first block for headed files.
//
// Geometry: SS, 40 tracks, 9x512, track size 0x1300; 1 reserved track; 2KB directory (4x512).

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	Tracks       = 40
	Sides        = 1
	SectorsPerTr = 9
	SectorSize   = 512
	TrackSize    = 0x1300 // 256 + 9*512

	BlockSizeBytes = 1024
	BlockSectors   = BlockSizeBytes / SectorSize // 2
	DirBlocks      = 2                           // 2KB directory = 2 x 1KB blocks
)

type CHS struct{ Track, Side, Sect byte }
type Disk struct{ Sectors [][][SectorSize]byte }
type DirEntry [32]byte

type FileItem struct {
	Name83 string
	Path   string
	Size   int64
	Data   []byte
}

// ----- 8.3 helpers -----
func to83(base string) string {
	name := strings.ToUpper(base)
	i := strings.LastIndex(name, ".")
	var fn, ext string
	if i >= 0 { fn, ext = name[:i], name[i+1:] } else { fn = name }
	filt := func(s string) string {
		var b strings.Builder
		for _, r := range s {
			if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_$~!#%&()@^{}'", r) {
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	fn, ext = filt(fn), filt(ext)
	if len(fn) == 0 { fn = "NONAME" }
	if len(fn) > 8 { fn = fn[:8] }
	if len(ext) > 3 { ext = ext[:3] }
	return fmt.Sprintf("%-8s%-3s", fn, ext)
}

// ----- +3DOS header -----
func le32(x int) [4]byte { return [4]byte{byte(x), byte(x>>8), byte(x>>16), byte(x>>24)} }
func le16(x int) [2]byte { return [2]byte{byte(x), byte(x>>8)} }

func plus3Header(body []byte, typ byte, p1, p2 int) []byte {
	h := make([]byte, 128)
	copy(h[0:], []byte("PLUS3DOS"))
	h[8] = 0x1A
	h[9] = 1
	h[10] = 0
	t := le32(len(body) + 128)
	h[11], h[12], h[13], h[14] = t[0], t[1], t[2], t[3]
	h[15] = typ
	bl := le16(len(body))
	p1le := le16(p1)
	p2le := le16(p2)
	h[16], h[17] = bl[0], bl[1]
	h[18], h[19] = p1le[0], p1le[1]
	h[20], h[21] = p2le[0], p2le[1]
	sum := 0
	for i := 0; i < 127; i++ { sum = (sum + int(h[i])) & 0xFF }
	h[127] = byte(sum)
	return h
}

func parseAtSuffix(base string) int {
	if i := strings.LastIndex(base, "@"); i >= 0 && i < len(base)-1 {
		num := base[i+1:]
		if j := strings.LastIndex(num, "."); j >= 0 { num = num[:j] }
		if n, err := strconv.Atoi(num); err == nil && n > 0 && n < 65536 {
			return n
		}
	}
	return 0
}

func chooseHeader(path string) (typ byte, p1, p2 int) {
	base := filepath.Base(path)
	ext := strings.ToUpper(filepath.Ext(base))
	override := parseAtSuffix(base)
	switch ext {
	case ".SCR":
		typ, p1, p2 = 3, 16384, 0
	case ".BAS":
		typ, p1, p2 = 0, 0x8000, 0
	case ".BIN", ".CODE":
		typ, p1, p2 = 3, 32768, 0
	default:
		typ, p1, p2 = 3, 32768, 0
	}
	if override != 0 { p1 = override }
	return
}

// ----- EDSK writer -----
func writeEDSK(w io.Writer, disk *Disk) error {
	hdr := make([]byte, 256)
	copy(hdr[0x00:], []byte("EXTENDED CPC DSK File\r\nDisk-Info\r\n"))
	copy(hdr[0x22:], []byte("zx3dsk+3 fix2"))
	hdr[0x30] = byte(Tracks)
	hdr[0x31] = byte(Sides)
	for i := 0; i < Tracks*Sides && 0x34+i < 256; i++ {
		hdr[0x34+i] = byte(TrackSize / 256)
	}
	if _, err := w.Write(hdr); err != nil { return err }

	for tr := 0; tr < Tracks; tr++ {
		th := make([]byte, 256)
		copy(th[0x00:], []byte("Track-Info\r\n"))
		th[0x10] = byte(tr) // C
		th[0x11] = 0x00     // H
		th[0x14] = 0x02     // N=2 -> 512
		th[0x15] = byte(SectorsPerTr)
		th[0x16] = 0x52     // GAP (R/W irrelevant here but common)
		th[0x17] = 0xE5     // filler

		for s := 0; s < SectorsPerTr; s++ {
			base := 0x18 + s*8
			th[base+0] = byte(tr)    // C
			th[base+1] = 0x00        // H
			th[base+2] = byte(s + 1) // R (1..9)
			th[base+3] = 0x02        // N
			th[base+4] = 0x00        // ST1
			th[base+5] = 0x00        // ST2
			th[base+6] = 0x00        // data length LE = 512
			th[base+7] = 0x02
		}
		if _, err := w.Write(th); err != nil { return err }
		for s := 0; s < SectorsPerTr; s++ {
			if _, err := w.Write(disk.Sectors[tr][s][:]); err != nil { return err }
		}
	}
	return nil
}

// ----- +3 filesystem builder -----
func buildDiskFromFolder(folder string) (*Disk, error) {
	d := &Disk{Sectors: make([][][SectorSize]byte, Tracks)}
	for t := 0; t < Tracks; t++ {
		d.Sectors[t] = make([][SectorSize]byte, SectorsPerTr)
		for s := 0; s < SectorsPerTr; s++ {
			for i := 0; i < SectorSize; i++ { d.Sectors[t][s][i] = 0xE5 }
		}
	}
	// +3/PCW 16-byte disk spec at T0,S1
	spec := make([]byte, 16)
	spec[0], spec[1], spec[2], spec[3] = 0, 0, 40, 9
	spec[4], spec[5], spec[6], spec[7] = 2, 1, 3, 2 // psh, reserved tracks, bsh, dir blocks
	spec[8], spec[9] = 0x2A, 0x52                  // gaps (rw=2A, format=52) per +3 docs
	copy(d.Sectors[0][0][:len(spec)], spec)

	// Collect files
	var items []FileItem
	err := filepath.WalkDir(folder, func(path string, de fs.DirEntry, err error) error {
		if err != nil { return err }
		if de.IsDir() { return nil }
		if de.Type().IsRegular() {
			b, err := os.ReadFile(path); if err != nil { return err }
			items = append(items, FileItem{ Path: path, Size: int64(len(b)), Data: b, Name83: filepath.Base(path) })
		}
		return nil
	})
	if err != nil { return nil, err }

	sort.Slice(items, func(i,j int) bool { return strings.ToLower(items[i].Name83) < strings.ToLower(items[j].Name83) })

	// 8.3 & dedupe
	used := map[string]int{}
	for i := range items {
		n := to83(items[i].Name83)
		base := strings.TrimRight(n[:8], " ")
		ext := strings.TrimRight(n[8:], " ")
		key := fmt.Sprintf("%-8s%-3s", base, ext)
		if used[key] > 0 {
			bb := []byte(fmt.Sprintf("%-8s", base))
			sfx := used[key] % 10; if sfx == 0 { sfx = 1 }
			bb[7] = byte('0' + sfx)
			key = fmt.Sprintf("%-8s%-3s", string(bb), ext)
		}
		used[key]++; items[i].Name83 = key
	}

	// Layout constants
	// Directory occupies first 2 * 1KB = 4 sectors on Track 1 (S1..S4).
	// In CP/M, allocation block numbers are absolute from the start of the data area
	// (after reserved tracks). Thus, block 0 and 1 are the directory; first file block is 2.
	dirSectors := DirBlocks * BlockSectors // 4 sectors = 2KB

	// Directory buffer (2KB) init to 0xE5
	dir := make([]byte, DirBlocks*BlockSizeBytes)
	for i := range dir { dir[i] = 0xE5 }
	dirIndex, maxDir := 0, len(dir)/32

	// Capacity (in 1KB blocks) across entire data area including the 2 directory blocks
	// Data area begins at Track 1, Sector 1.
	totalDataSectors := (Tracks-1) * SectorsPerTr // tracks 1..39 inclusive
	totalBlocks := totalDataSectors / BlockSectors // includes the 2 directory blocks (0 and 1)

	sectorAfter := func(tr, se, n int) (int,int) {
		se += n
		for se > SectorsPerTr { se -= SectorsPerTr; tr++ }
		return tr, se
	}
	// Map absolute allocation block number -> CHS list.
	blockToCHS := func(block int) ([]CHS, error) {
		if block < 0 || block >= totalBlocks { return nil, errors.New("block OOR") }
		// Start of data area = Track 1, Sector 1.
		absSectors := block * BlockSectors
		tr, se := 1, 1
		tr, se = sectorAfter(tr, se, absSectors)
		chs := make([]CHS, BlockSectors)
		for i:=0;i<BlockSectors;i++ {
			chs[i] = CHS{Track: byte(tr), Side: 0, Sect: byte(se)}
			tr, se = sectorAfter(tr, se, 1)
		}
		return chs, nil
	}
	nextBlock := DirBlocks // first allocatable
	writeBlock := func(block int, data []byte) error {
		chs, err := blockToCHS(block); if err != nil { return err }
		off := 0
		for _, c := range chs {
			chunk := SectorSize; if off+chunk > len(data) { chunk = len(data)-off }
			if chunk > 0 {
				copy(d.Sectors[int(c.Track)][int(c.Sect-1)][:chunk], data[off:off+chunk])
				off += chunk
			}
		}
		return nil
	}
	putDir := func(idx int, e DirEntry) { copy(dir[idx*32:(idx+1)*32], e[:]) }
	alloc := func(n int) ([]int, error) {
		if nextBlock+n > totalBlocks { return nil, errors.New("disk full") }
		blocks := make([]int, n)
		for i:=0;i<n;i++ { blocks[i] = nextBlock+i }
		nextBlock += n; return blocks, nil
	}

	for _, it := range items {
		typ, p1, p2 := chooseHeader(it.Path)
		h := plus3Header(it.Data, typ, p1, p2)
		data := append(h, it.Data...)
		total := len(data)

		if dirIndex >= maxDir { fmt.Fprintf(os.Stderr, "Directory full; skipping %s\n", it.Path); continue }
		if total == 0 { putDir(dirIndex, makeDirEntry(it.Name83, 0, 0, nil)); dirIndex++; continue }

		var pos int
		extentNo := 0
		for pos < total {
			remain := total - pos
			bytesThis := remain
			if bytesThis > 16*1024 { bytesThis = 16 * 1024 }
			need := (bytesThis + BlockSizeBytes - 1) / BlockSizeBytes
			blocks, err := alloc(need)
			if err != nil { fmt.Fprintf(os.Stderr, "Disk full; truncating %s\n", it.Path); break }
			for i, b := range blocks {
				start := pos + i*BlockSizeBytes
				end := start + BlockSizeBytes
				if end > total { end = total }
				if start >= end { break }
				if err := writeBlock(b, data[start:end]); err != nil { return nil, err }
			}
			rc := byte((bytesThis + 127)/128)
			putDir(dirIndex, makeDirEntry(it.Name83, extentNo, rc, blocks))
			dirIndex++
			pos += bytesThis
			extentNo++
		}
	}

	// Write directory (T1, S1..S4)
	dirOff := 0
	for s := 1; s <= dirSectors; s++ {
		copy(d.Sectors[1][s-1][:], dir[dirOff:dirOff+SectorSize]); dirOff += SectorSize
	}
	return d, nil
}

func makeDirEntry(name83 string, extent int, rc byte, blocks []int) DirEntry {
	var e DirEntry
	e[0] = 0 // user 0
	fn := fmt.Sprintf("%-11s", strings.ToUpper(name83))
	copy(e[1:12], []byte(fn[:11]))
	e[12] = byte(extent & 0x1F)        // EX low 5 bits
	e[13] = byte((extent >> 5) & 0x07) // S1 high bits of extent (CP/M 2.2)
	e[14] = 0x00                       // S2 (unused for small files)
	e[15] = rc
	for i := 0; i < 16 && i < len(blocks); i++ {
		e[16+i] = byte(blocks[i]) // absolute allocation block numbers (including dir blocks)
	}
	return e
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "Usage: %s <folder> <out.dsk>\n", os.Args[0])
		os.Exit(2)
	}
	in, out := os.Args[1], os.Args[2]
	info, err := os.Stat(in)
	if err != nil || !info.IsDir() { fmt.Fprintf(os.Stderr, "Input must be a folder\n"); os.Exit(1) }

	disk, err := buildDiskFromFolder(in)
	if err != nil { fmt.Fprintf(os.Stderr, "Build error: %v\n", err); os.Exit(1) }

	var buf bytes.Buffer
	if err := writeEDSK(&buf, disk); err != nil { fmt.Fprintf(os.Stderr, "Write EDSK error: %v\n", err); os.Exit(1) }

	if err := os.WriteFile(out, buf.Bytes(), 0644); err != nil { fmt.Fprintf(os.Stderr, "Save error: %v\n", err); os.Exit(1) }
	fmt.Printf("Wrote %s (%d bytes)\n", out, buf.Len())
}
