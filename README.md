# zx3dsk
### ZX Spectrum +3 Disk Image Tools

A collection of utilities for creating and inspecting ZX Spectrum +3 disk images in EDSK format, compatible with CP/M and +3DOS file systems.

- zx3dsk: Create .dsk from folder contents
- zx3info: Display contents of .dsk file
- zx3extract: Extract the contents of a .dsk file to a folder

## Tools

### zx3dsk - Disk Image Creator

Creates ZX Spectrum +3 compatible disk images from a folder of files.

**Usage:**
```bash
go run zx3dsk.go <folder> <output.dsk>
```

**Features:**
- Generates EDSK format disk images compatible with ZX Spectrum +3 emulators
- Automatically adds +3DOS headers to files based on extension
- Supports proper CP/M directory allocation and block mapping
- Handles 8.3 filename conversion with collision detection
- Creates 40-track, single-sided, 9-sector format (180KB capacity)

**File Type Detection:**
- `.BAS` → BASIC program (type 0, load address 0x8000)
- `.SCR` → Screen dump (type 3, load address 16384)
- `.BIN`, `.CODE` → Binary code (type 3, load address 32768)
- `@nnnnn` suffix → Override load address (e.g., `game@49152.bin`)

### zx3info - Disk Image Inspector

Analyzes and displays information about ZX Spectrum disk images.

**Usage:**
```bash
go run zx3info.go <image.dsk>
```

**Features:**
- Supports both Standard and Extended DSK formats
- Detects +3DOS/PCW-180K disk layout
- Lists directory contents with file details
- Shows disk geometry and track information
- Displays raw directory entries with extent and block information

### zx3extract - File Extractor

Extracts files from ZX Spectrum +3 disk images to a folder.

**Usage:**
```bash
go run zx3extract.go [-keepheader] [-meta] <image.dsk> <outdir>
```

**Options:**
- `-keepheader` - Preserve +3DOS 128-byte headers (default: strip headers)
- `-meta` - Generate JSON metadata files alongside extracted files

**Features:**
- Reconstructs files from CP/M extents and allocation blocks
- Automatically detects and handles +3DOS headers
- Generates detailed metadata including directory info and header fields
- Supports multi-extent files with proper ordering
- Creates clean output filenames from 8.3 format

## Technical Details

### Disk Format
- **Geometry:** 40 tracks, 1 side, 9 sectors per track (512 bytes each)
- **Capacity:** ~180KB total, ~176KB usable
- **File System:** CP/M compatible with +3DOS extensions
- **Directory:** 2KB (4 sectors) starting at Track 1, Sector 1

### +3DOS Headers
All files receive appropriate 128-byte +3DOS headers containing:
- File type identification
- Load address and execution parameters
- File length information
- Checksum validation

### Compatibility
- ZX Spectrum +3 hardware and emulators
- Amstrad PCW series (180K format)
- CP/M 2.2 compatible systems
- Modern emulators supporting EDSK format
- Images tested with ZEsarUX
## Building

```bash
# Build disk creator
go build -o zx3dsk zx3dsk.go

# Build disk inspector  
go build -o zx3info zx3info.go

# Build file extractor
go build -o zx3extract zx3extract.go
```

## Example Usage

```bash
# Create a disk image from a folder
./zx3dsk ./my-spectrum-files output.dsk

# Inspect the created disk image
./zx3info output.dsk

# Extract files from a disk image
./zx3extract input.dsk ./extracted-files

# Extract files with metadata and keep +3DOS headers
./zx3extract -keepheader -meta input.dsk ./extracted-files
```

## Requirements

- Go 1.16 or later
- No external dependencies

## Author

**Email:** h@ual.fi  
**Social:** https://oldbytes.space/@haitchfive

## License

This project is licensed under the Apache License 2.0 - see the [LICENSE](LICENSE) file for details.
