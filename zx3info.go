package main

// zx3info_plus3_spec: Inspector aligned with SpecIDE track handling.
// SpecIDE is an excellent emulator by MartianGirl
// https://codeberg.org/MartianGirl/SpecIde

// - Detects STANDARD and EXTENDED DSK.
// - Uses the track size table to decide whether a track exists; if size==0, skip reading it.
// - For each existing track, reads one 256-byte "Track-Info\r\n" header, then N sector entries.
// - For each sector, uses the 16-bit data length when present; otherwise falls back to 128<<N.
// - For +3 directory listing: require +3 spec at T0,S1 and 512B sectors for T1 S1..S4.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

type diskType int

const (
	dskUnknown diskType = iota
	dskStandard
	dskExtended
)

type secHeader struct {
	C, H, R, N, ST1, ST2 byte
	DataLen              uint16
}
type sector struct {
	R    int
	Data []byte
}
type track struct {
	Sectors []sector
	ByID    map[int]*sector
}
type disk struct {
	kind      diskType
	tracks    int
	sides     int
	trackSize []int
	Tracks    []track // cylinder index -> track
}

// --- helpers ---
func readExactly(r io.Reader, n int) ([]byte, error) {
	buf := make([]byte, n)
	_, err := io.ReadFull(r, buf)
	return buf, err
}

// --- parser ---
func parseDSK(path string) (*disk, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	hdr, err := readExactly(f, 256)
	if err != nil {
		return nil, err
	}

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
	sides := int(hdr[0x31])
	if tracks <= 0 || sides <= 0 {
		return nil, fmt.Errorf("bad tracks/sides %d/%d", tracks, sides)
	}

	// Build track size table
	total := tracks * sides
	ts := make([]int, total)
	if kind == dskExtended {
		if 0x34+total > 256 {
			return nil, errors.New("invalid track size table")
		}
		for i := 0; i < total; i++ {
			ts[i] = int(hdr[0x34+i]) * 256
		}
	} else {
		sizeLE := binary.LittleEndian.Uint16(hdr[0x32:0x34])
		if sizeLE == 0 {
			sizeLE = 0x1300
		}
		for i := 0; i < total; i++ {
			ts[i] = int(sizeLE)
		}
	}

	d := &disk{kind: kind, tracks: tracks, sides: sides, trackSize: ts, Tracks: make([]track, tracks)}

	// Read tracks one by one using sizes
	for t := 0; t < total; t++ {
		size := ts[t]
		if size == 0 {
			// Unformatted/missing track: skip
			continue
		}
		th, err := readExactly(f, 256)
		if err != nil {
			return nil, fmt.Errorf("track %d: %w", t, err)
		}
		if !bytes.HasPrefix(th, []byte("Track-Info\r\n")) {
			return nil, fmt.Errorf("track %d: missing Track-Info header", t)
		}
		secCount := int(th[0x15])
		if secCount <= 0 {
			return nil, fmt.Errorf("track %d: bad sector count", t)
		}
		off := 0x18
		headers := make([]secHeader, secCount)
		for i := 0; i < secCount; i++ {
			headers[i] = secHeader{
				C: th[off+0], H: th[off+1], R: th[off+2], N: th[off+3],
				ST1: th[off+4], ST2: th[off+5],
				DataLen: binary.LittleEndian.Uint16(th[off+6 : off+8]),
			}
			off += 8
		}
		trk := track{Sectors: make([]sector, secCount), ByID: map[int]*sector{}}
		read := 256
		for i := 0; i < secCount; i++ {
			want := int(headers[i].DataLen)
			if want == 0 {
				want = 128 << headers[i].N
			}
			if want < 0 {
				return nil, fmt.Errorf("track %d sector %d: bad length", t, i+1)
			}
			payload, err := readExactly(f, want)
			if err != nil {
				return nil, fmt.Errorf("track %d: %w", t, err)
			}
			read += want
			trk.Sectors[i] = sector{R: int(headers[i].R), Data: payload}
			trk.ByID[int(headers[i].R)] = &trk.Sectors[i]
		}
		// Skip padding to declared track size
		pad := size - read
		if pad > 0 {
			_, _ = readExactly(f, pad)
		}
		// Map t back to cylinder (SS: t==cyl)
		cyl := t
		if cyl < len(d.Tracks) {
			d.Tracks[cyl] = trk
		}
	}

	return d, nil
}

// --- +3 directory helpers ---
func specT0S1(d *disk) []byte {
	if len(d.Tracks) == 0 {
		return nil
	}
	s := d.Tracks[0].ByID[1]
	if s == nil || len(s.Data) < 16 {
		return nil
	}
	return s.Data[:16]
}
func looksPlus3Spec(b []byte) bool {
	return b != nil && len(b) >= 16 && b[0] == 0 && (b[1] == 0 || b[1] == 1) && b[2] >= 40 && b[3] >= 9 && b[4] == 2 && b[6] == 3 && b[7] == 2
}

type dirEntry struct {
	User           byte
	Name, Ext      string
	EX, S1, S2, RC byte
	Blocks         []byte
}

func dirSectors(d *disk) ([][]byte, error) {
	if len(d.Tracks) < 2 {
		return nil, errors.New("no track 1")
	}
	tr1 := d.Tracks[1]
	secs := make([][]byte, 4)
	for i := 1; i <= 4; i++ {
		s := tr1.ByID[i]
		if s == nil {
			return nil, fmt.Errorf("missing directory R%d", i)
		}
		if len(s.Data) != 512 {
			return nil, fmt.Errorf("directory R%d len=%d (need 512)", i, len(s.Data))
		}
		secs[i-1] = s.Data
	}
	return secs, nil
}

func parseDir(secs [][]byte) []dirEntry {
	buf := bytes.Join(secs, nil)
	var out []dirEntry
	for i := 0; i+32 <= len(buf); i += 32 {
		e := buf[i : i+32]
		if e[0] == 0xE5 {
			continue
		}
		out = append(out, dirEntry{
			User: e[0],
			Name: strings.TrimRight(string(e[1:9]), " "),
			Ext:  strings.TrimRight(string(e[9:12]), " "),
			EX:   e[12], S1: e[13], S2: e[14], RC: e[15],
			Blocks: append([]byte(nil), e[16:32]...),
		})
	}
	return out
}

type fileAgg struct {
	User      byte
	Name, Ext string
	Extents   []dirEntry
	Bytes     int
}

func aggregate(entries []dirEntry) []fileAgg {
	type key struct {
		User      byte
		Name, Ext string
	}
	g := map[key][]dirEntry{}
	for _, e := range entries {
		g[key{e.User, e.Name, e.Ext}] = append(g[key{e.User, e.Name, e.Ext}], e)
	}
	var out []fileAgg
	for k, exts := range g {
		sort.Slice(exts, func(i, j int) bool {
			ai := int(exts[i].S1)<<5 | int(exts[i].EX&0x1F)
			aj := int(exts[j].S1)<<5 | int(exts[j].EX&0x1F)
			return ai < aj
		})
		total := 0
		for _, e := range exts {
			total += int(e.RC) * 128
		}
		out = append(out, fileAgg{User: k.User, Name: k.Name, Ext: k.Ext, Extents: exts, Bytes: total})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].User != out[j].User {
			return out[i].User < out[j].User
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Ext < out[j].Ext
	})
	return out
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <image.dsk>\n", os.Args[0])
		os.Exit(2)
	}
	path := os.Args[1]
	d, err := parseDSK(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Parse error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Disk: %s\n", path)
	fmt.Printf(" Type: %s  Tracks: %d  Sides: %d\n",
		map[diskType]string{dskStandard: "Standard", dskExtended: "Extended"}[d.kind], d.tracks, d.sides)

	spec := specT0S1(d)
	if !looksPlus3Spec(spec) {
		fmt.Println(" Not a +3 (PCW-180K) layout or missing +3 spec at T0,S1. Showing geometry only.")
		return
	}
	secs, err := dirSectors(d)
	if err != nil {
		fmt.Printf(" +3 spec found but directory not in +3 default layout: %v\n", err)
		return
	}
	entries := parseDir(secs)
	if len(entries) == 0 {
		fmt.Println(" Directory: (empty)")
		return
	}

	fmt.Println("\nRaw directory entries:")
	fmt.Println(" User  Name       Ext  Extent  RC   Blocks")
	for _, e := range entries {
		extentNum := int(e.S1)<<5 | int(e.EX&0x1F)
		var blkIdxs []string
		for _, b := range e.Blocks {
			if b != 0 {
				blkIdxs = append(blkIdxs, fmt.Sprintf("%d", int(b)))
			}
		}
		fmt.Printf("  %3d  %-8s   %-3s  %5d  %3d  %s\n", int(e.User), e.Name, e.Ext, extentNum, int(e.RC), strings.Join(blkIdxs, ","))
	}
}
