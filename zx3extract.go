package main

// zx3extract v2: Extract ZX Spectrum +3 DSK images to a folder.
// Adds -meta flag to write a JSON metadata file alongside each extracted file.
// Metadata includes CP/M directory info and +3DOS header fields (when present).
//
// Build: go build -o zx3extract zx3extract.go
// Usage: ./zx3extract <image.dsk> <outdir> [-keepheader] [-meta]

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type diskType int
const (
	dskUnknown diskType = iota
	dskStandard
	dskExtended
)

type secHeader struct{ C,H,R,N,ST1,ST2 byte; DataLen uint16 }
type sector struct{ R int; Data []byte }
type track struct{ Sectors []sector; ByID map[int]*sector }
type disk struct {
	kind   diskType
	tracks int
	sides  int
	trackSize []int
	Tracks []track // cylinder index -> track
}

func readExactly(r io.Reader, n int) ([]byte, error) { buf := make([]byte, n); _, err := io.ReadFull(r, buf); return buf, err }

func parseDSK(path string) (*disk, error) {
	f, err := os.Open(path); if err != nil { return nil, err }
	defer f.Close()

	hdr, err := readExactly(f, 256); if err != nil { return nil, err }

	var kind diskType
	switch {
	case bytes.HasPrefix(hdr, []byte("EXTENDED CPC DSK File\r\nDisk-Info\r\n")):
		kind = dskExtended
	case bytes.HasPrefix(hdr, []byte("MV - CPCEMU Disk-File\r\nDisk-Info\r\n")):
		kind = dskStandard
	default:
		return nil, errors.New("not a DSK (unknown header)")
	}

	tracks := int(hdr[0x30])
	sides  := int(hdr[0x31])
	if tracks <= 0 || sides <= 0 { return nil, fmt.Errorf("bad tracks/sides %d/%d", tracks, sides) }

	// Build track size table
	total := tracks * sides
	ts := make([]int, total)
	if kind == dskExtended {
		if 0x34+total > 256 { return nil, errors.New("invalid track size table") }
		for i := 0; i < total; i++ { ts[i] = int(hdr[0x34+i]) * 256 }
	} else {
		sizeLE := binary.LittleEndian.Uint16(hdr[0x32:0x34]); if sizeLE == 0 { sizeLE = 0x1300 }
		for i := 0; i < total; i++ { ts[i] = int(sizeLE) }
	}

	d := &disk{ kind: kind, tracks: tracks, sides: sides, trackSize: ts, Tracks: make([]track, tracks) }

	// Read tracks one by one using sizes
	for t := 0; t < total; t++ {
		size := ts[t]
		if size == 0 {
			// Unformatted/missing track: skip
			continue
		}
		th, err := readExactly(f, 256); if err != nil { return nil, fmt.Errorf("track %d: %w", t, err) }
		if !bytes.HasPrefix(th, []byte("Track-Info\r\n")) { return nil, fmt.Errorf("track %d: missing Track-Info header", t) }
		secCount := int(th[0x15]); if secCount <= 0 { return nil, fmt.Errorf("track %d: bad sector count", t) }
		off := 0x18
		headers := make([]secHeader, secCount)
		for i := 0; i < secCount; i++ {
			headers[i] = secHeader{
				C: th[off+0], H: th[off+1], R: th[off+2], N: th[off+3],
				ST1: th[off+4], ST2: th[off+5],
				DataLen: binary.LittleEndian.Uint16(th[off+6:off+8]),
			}
			off += 8
		}
		trk := track{ Sectors: make([]sector, secCount), ByID: map[int]*sector{} }
		read := 256
		for i := 0; i < secCount; i++ {
			want := int(headers[i].DataLen); if want == 0 { want = 128 << headers[i].N }
			if want < 0 { return nil, fmt.Errorf("track %d sector %d: bad length", t, i+1) }
			payload, err := readExactly(f, want); if err != nil { return nil, fmt.Errorf("track %d: %w", t, err) }
			read += want
			trk.Sectors[i] = sector{ R:int(headers[i].R), Data: payload }
			trk.ByID[int(headers[i].R)] = &trk.Sectors[i]
		}
		// Skip padding to declared track size
		pad := size - read
		if pad > 0 { _, _ = readExactly(f, pad) }
		// Map t back to cylinder (SS: t==cyl)
		cyl := t
		if cyl < len(d.Tracks) { d.Tracks[cyl] = trk }
	}

	return d, nil
}

// --- +3 helpers ---
func specT0S1(d *disk) []byte {
	if len(d.Tracks) == 0 { return nil }
	s := d.Tracks[0].ByID[1]; if s == nil || len(s.Data) < 16 { return nil }
	return s.Data[:16]
}
func looksPlus3Spec(b []byte) bool {
	return b!=nil && len(b)>=16 && b[0]==0 && (b[1]==0||b[1]==1) && b[2]>=40 && b[3]>=9 && b[4]==2 && b[6]==3 && b[7]==2
}

type dirEntry struct{ User byte; Name, Ext string; EX,S1,S2,RC byte; Blocks []byte }

func dirSectors(d *disk) ([][]byte, error) {
	if len(d.Tracks) < 2 { return nil, errors.New("no track 1") }
	tr1 := d.Tracks[1]; secs := make([][]byte, 4)
	for i := 1; i <= 4; i++ {
		s := tr1.ByID[i]; if s == nil { return nil, fmt.Errorf("missing directory R%d", i) }
		if len(s.Data) != 512 { return nil, fmt.Errorf("directory R%d len=%d (need 512)", i, len(s.Data)) }
		secs[i-1] = s.Data
	}
	return secs, nil
}

func parseDir(secs [][]byte) []dirEntry {
	buf := bytes.Join(secs, nil); var out []dirEntry
	for i:=0; i+32 <= len(buf); i+=32 {
		e := buf[i:i+32]; if e[0] == 0xE5 { continue }
		out = append(out, dirEntry{
			User: e[0],
			Name: strings.TrimRight(string(e[1:9]), " "),
			Ext:  strings.TrimRight(string(e[9:12]), " "),
			EX:e[12], S1:e[13], S2:e[14], RC:e[15],
			Blocks: append([]byte(nil), e[16:32]...),
		})
	}
	return out
}

type extentKey struct{ EX, S1 byte }
type fileAgg struct{ User byte; Name, Ext string; Extents map[extentKey]dirEntry; Order []extentKey; TotalBytes int }

func aggregate(entries []dirEntry) []fileAgg {
	type key struct{ User byte; Name, Ext string }
	group := map[key][]dirEntry{}
	for _, e := range entries {
		group[key{e.User, e.Name, e.Ext}] = append(group[key{e.User, e.Name, e.Ext}], e)
	}
	var out []fileAgg
	for k, list := range group {
		// order by (S1<<5)|(EX&0x1F)
		sort.Slice(list, func(i,j int) bool {
			ai := int(list[i].S1)<<5 | int(list[i].EX&0x1F)
			aj := int(list[j].S1)<<5 | int(list[j].EX&0x1F)
			return ai < aj
		})
		m := make(map[extentKey]dirEntry)
		var ord []extentKey
		total := 0
		for _, e := range list {
			kx := extentKey{EX:e.EX, S1:e.S1}
			m[kx] = e
			ord = append(ord, kx)
			total += int(e.RC) * 128
		}
		out = append(out, fileAgg{ User:k.User, Name:k.Name, Ext:k.Ext, Extents:m, Order:ord, TotalBytes: total })
	}
	// stable order
	sort.Slice(out, func(i,j int) bool {
		if out[i].User != out[j].User { return out[i].User < out[j].User }
		if out[i].Name != out[j].Name { return out[i].Name < out[j].Name }
		return out[i].Ext < out[j].Ext
	})
	return out
}

// Map absolute block number (0-based from start of data area) to bytes from the disk image.
// Data area starts at Track 1, Sector 1.
func getBlock(d *disk, block int) ([]byte, error) {
	// 1KB block = 2 sectors of 512
	startTr, startSe := 1, 1
	// add block*2 sectors
	advance := block * 2
	tr, se := startTr, startSe
	for advance > 0 {
		se++
		if se > 9 { se = 1; tr++ }
		advance--
	}
	// read two sectors
	var out bytes.Buffer
	for i := 0; i < 2; i++ {
		if tr >= len(d.Tracks) { return nil, fmt.Errorf("block %d OOR (tr=%d)", block, tr) }
		sec := d.Tracks[tr].ByID[se]
		if sec == nil { return nil, fmt.Errorf("missing sector T%d R%d", tr, se) }
		if len(sec.Data) != 512 { return nil, fmt.Errorf("sector T%d R%d len=%d", tr, se, len(sec.Data)) }
		out.Write(sec.Data)
		// advance to next sector
		se++
		if se > 9 { se = 1; tr++ }
	}
	return out.Bytes(), nil
}

// +3DOS header metadata container
type Plus3Header struct {
	Signature   string `json:"signature"`
	Issue       uint8  `json:"issue"`
	Version     uint8  `json:"version"`
	TotalLength int    `json:"total_length"`
	Type        uint8  `json:"type"`
	BasicType   string `json:"basic_type"`
	DataLength  int    `json:"data_length"`
	Param1      int    `json:"param1"`
	Param2      int    `json:"param2"`
	Checksum    uint8  `json:"checksum"`
	ChecksumOK  bool   `json:"checksum_ok"`
	LoadAddress int    `json:"load_address,omitempty"`
}

// Detect +3DOS header and (optionally) strip it. Returns data, header meta (or nil), and a boolean indicating header presence.
func peelPlus3Header(b []byte) ([]byte, *Plus3Header, bool) {
	if len(b) < 128 { return b, nil, false }
	h := b[:128]
	if !bytes.Equal(h[0:8], []byte("PLUS3DOS")) { return b, nil, false }
	if h[8] != 0x1A { return b, nil, false }
	sum := 0
	for i := 0; i < 127; i++ { sum = (sum + int(h[i])) & 0xFF }
	totalLen := int(h[11]) | int(h[12])<<8 | int(h[13])<<16 | int(h[14])<<24
	dataLen := int(binary.LittleEndian.Uint16(h[16:18]))
	p1 := int(binary.LittleEndian.Uint16(h[18:20]))
	p2 := int(binary.LittleEndian.Uint16(h[20:22]))
	typ := h[15]
	btype := map[byte]string{0:"program",1:"numeric_array",2:"char_array",3:"code_or_screen"}[typ]
	meta := &Plus3Header{
		Signature: "PLUS3DOS",
		Issue: h[9], Version: h[10],
		TotalLength: totalLen,
		Type: typ, BasicType: btype,
		DataLength: dataLen, Param1: p1, Param2: p2,
		Checksum: h[127], ChecksumOK: byte(sum) == h[127],
	}
	if typ == 3 { meta.LoadAddress = p1 }
	if totalLen < 128 || dataLen < 0 || totalLen-128 < dataLen {
		// suspicious, but still treat as header and return best-effort
	}
	if 128+dataLen > len(b) { dataLen = len(b)-128 }
	return b[128:128+dataLen], meta, true
}

type ExtentMeta struct {
	Extent int    `json:"extent"`
	RC     int    `json:"rc"`
	Blocks []int  `json:"blocks"`
}

type FileMeta struct {
	User       int          `json:"user"`
	Name       string       `json:"name"`
	Ext        string       `json:"ext"`
	TotalBytes int          `json:"total_bytes_from_rc"`
	Extents    []ExtentMeta `json:"extents"`
	Plus3      *Plus3Header `json:"plus3_header,omitempty"`
	OutputName string       `json:"output_name"`
	OutputSize int          `json:"output_size"`
	HeaderKept bool         `json:"header_kept"`
}

func main() {
	flagKeep := flag.Bool("keepheader", false, "keep +3DOS 128-byte headers (default: strip if present)")
	flagMeta := flag.Bool("meta", false, "write a .json metadata file alongside each extracted file")
	flag.Parse()
	if flag.NArg() != 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <image.dsk> <outdir> [-keepheader] [-meta]\n", os.Args[0])
		os.Exit(2)
	}
	image := flag.Arg(0)
	outdir := flag.Arg(1)

	if err := os.MkdirAll(outdir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Output dir error: %v\n", err)
		os.Exit(1)
	}

	d, err := parseDSK(image); if err != nil {
		fmt.Fprintf(os.Stderr, "Parse error: %v\n", err)
		os.Exit(1)
	}
	// Ensure +3 layout present
	spec := specT0S1(d)
	if !looksPlus3Spec(spec) {
		fmt.Fprintf(os.Stderr, "Warning: not a +3 PCW-180K layout (missing +3 spec at T0,S1). Attempting anyway...\n")
	}
	secs, err := dirSectors(d)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Directory not found in standard +3 location: %v\n", err)
		os.Exit(1)
	}
	entries := parseDir(secs)
	if len(entries) == 0 {
		fmt.Println("No files found.")
		return
	}
	files := aggregate(entries)

	for _, f := range files {
		// reconstruct bytes extent-by-extent
		var assembled bytes.Buffer
		var extentMetas []ExtentMeta
		for _, k := range f.Order {
			e := f.Extents[k]
			extentNum := int(e.S1)<<5 | int(e.EX&0x1F)
			// load each listed block (non-zero bytes indicate block numbers; zero may mean "unused")
			var extBytes bytes.Buffer
			var blocks []int
			for _, b := range e.Blocks {
				if b == 0 { continue } // zero indicates no block / padding in entry
				blocks = append(blocks, int(b))
				chunk, err := getBlock(d, int(b))
				if err != nil { fmt.Fprintf(os.Stderr, "Block read err for %s.%s: %v\n", f.Name, f.Ext, err); break }
				extBytes.Write(chunk)
			}
			// respect RC (records of 128 bytes)
			want := int(e.RC) * 128
			if want > extBytes.Len() { want = extBytes.Len() }
			assembled.Write(extBytes.Bytes()[:want])

			extentMetas = append(extentMetas, ExtentMeta{
				Extent: extentNum,
				RC: int(e.RC),
				Blocks: blocks,
			})
		}
		fileBytes := assembled.Bytes()

		// Prepare names
		base := strings.TrimRight(f.Name, " ")
		ext  := strings.TrimRight(f.Ext, " ")
		if base == "" { base = "NONAME" }
		saveName := fmt.Sprintf("%s.%s", base, ext)
		savePath := filepath.Join(outdir, saveName)

		// Detect +3 header and optionally strip
		outData := fileBytes
		var plus3 *Plus3Header
		var hadHeader bool
		if data, hdr, ok := peelPlus3Header(fileBytes); ok {
			plus3, hadHeader = hdr, true
			if !*flagKeep {
				outData = data
			}
		}

		// Write file
		if err := os.WriteFile(savePath, outData, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Write error %s: %v\n", saveName, err)
			continue
		}
		fmt.Printf("Extracted %s (%d bytes)\n", saveName, len(outData))

		// Write metadata JSON when requested
		if *flagMeta {
			meta := FileMeta{
				User: int(f.User), Name: base, Ext: ext,
				TotalBytes: f.TotalBytes,
				Extents: extentMetas,
				Plus3: plus3,
				OutputName: saveName,
				OutputSize: len(outData),
				HeaderKept: *flagKeep && hadHeader,
			}
			js, err := json.MarshalIndent(meta, "", "  ")
			if err == nil {
				jsonPath := savePath + ".json"
				_ = os.WriteFile(jsonPath, js, 0644)
			}
		}
	}
}
