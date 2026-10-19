package process

import (
	"bytes"
	"debug/macho"
	"encoding/binary"
	"io"
	"os"
)

const (
	loadCodeSignature  = 0x1d
	blobCodeDirectory  = 0xfade0c02
	blobEntitlements   = 0xfade7171
	flagRuntime        = 0x10000
	entitlementGetTask = "com.apple.security.get-task-allow"
)

// refusesDebugger reports whether the macOS executable at path is signed with the
// hardened runtime and without the get-task-allow entitlement, which makes macOS refuse
// to let a debugger attach. It reports false for other executables.
func refusesDebugger(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false
	}
	var r io.ReaderAt = f
	var offset int64
	exe, err := macho.NewFile(f)
	if err != nil {
		fat, err := macho.NewFatFile(f)
		if err != nil || len(fat.Arches) == 0 {
			return false
		}
		exe, offset = fat.Arches[0].File, int64(fat.Arches[0].Offset)
	}
	for _, load := range exe.Loads {
		raw := load.Raw()
		if len(raw) < 16 || exe.ByteOrder.Uint32(raw) != loadCodeSignature {
			continue
		}
		start, size := offset+int64(exe.ByteOrder.Uint32(raw[8:])), int64(exe.ByteOrder.Uint32(raw[12:]))
		if start+size > info.Size() {
			return false
		}
		sig := make([]byte, size)
		if _, err := r.ReadAt(sig, start); err != nil {
			return false
		}
		return hardened(sig)
	}
	return false
}

// hardened reports whether the code signature sig sets the hardened runtime flag and
// grants no get-task-allow entitlement. A signature is a big-endian superblob that
// indexes blobs by offset.
func hardened(sig []byte) bool {
	be := binary.BigEndian
	if len(sig) < 12 {
		return false
	}
	var runtime, getTask bool
	for i := range be.Uint32(sig[8:]) {
		entry := 12 + 8*int(i)
		if entry+8 > len(sig) {
			return false
		}
		blob := int(be.Uint32(sig[entry+4:]))
		if blob+16 > len(sig) {
			continue
		}
		switch be.Uint32(sig[blob:]) {
		case blobCodeDirectory:
			runtime = be.Uint32(sig[blob+12:])&flagRuntime != 0
		case blobEntitlements:
			end := min(blob+int(be.Uint32(sig[blob+4:])), len(sig))
			_, value, ok := bytes.Cut(sig[blob:end], []byte("<key>"+entitlementGetTask+"</key>"))
			getTask = ok && bytes.HasPrefix(bytes.TrimSpace(value), []byte("<true/>"))
		}
	}
	return runtime && !getTask
}
