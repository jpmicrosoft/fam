package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"io"
	"os"

	errs "foundry-agent-manager/internal/errors"
)

func (u *Updater) extract(ctx context.Context, data []byte) ([]byte, error) {
	if u.goos == "windows" {
		return u.extractZIP(ctx, data)
	}
	return u.extractTar(ctx, data)
}

type entries struct {
	u          *Updater
	seen       map[string]bool
	executable []byte
	expanded   int64
}

func (e *entries) read(ctx context.Context, name string, mode os.FileMode, reader io.Reader) error {
	if !isSafeRootName(name) || !mode.IsRegular() || e.seen[name] ||
		len(e.seen) >= e.u.limits.entries {
		return errs.Security("release archive contains an unsafe, duplicate, or excessive entry")
	}
	limit := e.u.limits.notice
	switch name {
	case executableName(e.u.goos):
		limit = e.u.limits.executable
	case "LICENSE", "THIRD_PARTY_NOTICES.txt":
	default:
		return errs.Security("release archive contains an unexpected root entry")
	}
	if remaining := e.u.limits.expanded - e.expanded; remaining < limit {
		limit = remaining
	}
	if limit < 0 {
		return errs.Security("release archive exceeds its expanded size limit")
	}
	if name == executableName(e.u.goos) {
		data, err := boundedRead(ctx, reader, limit)
		if err != nil {
			return archiveError(ctx, err)
		}
		if len(data) == 0 {
			return errs.Security("release executable is empty")
		}
		e.executable = data
		e.expanded += int64(len(data))
	} else {
		count, err := io.Copy(io.Discard, io.LimitReader(contextReader{ctx, reader}, limit+1))
		if err != nil || count > limit {
			return archiveError(ctx, err)
		}
		e.expanded += count
	}
	e.seen[name] = true
	return nil
}

func (e *entries) finish() ([]byte, error) {
	if !e.seen[executableName(e.u.goos)] || !e.seen["LICENSE"] || !e.seen["THIRD_PARTY_NOTICES.txt"] {
		return nil, errs.Security("release archive must contain exactly one root executable, LICENSE, and THIRD_PARTY_NOTICES.txt")
	}
	return e.executable, nil
}

func (u *Updater) extractZIP(ctx context.Context, data []byte) ([]byte, error) {
	if err := validateZIPDirectory(data, u.limits.entries); err != nil {
		return nil, err
	}
	archive, err := zip.NewReader(contextReaderAt{ctx, bytes.NewReader(data)}, int64(len(data)))
	if err != nil {
		return nil, archiveError(ctx, err)
	}
	if len(archive.File) > u.limits.entries {
		return nil, errs.Security("release ZIP exceeds its entry count limit")
	}
	entries := entries{u: u, seen: map[string]bool{}}
	for _, file := range archive.File {
		if file.Flags&1 != 0 || file.UncompressedSize64 > uint64(u.limits.expanded) {
			return nil, errs.Security("release ZIP has an encrypted or oversized entry")
		}
		reader, err := file.Open()
		if err != nil {
			return nil, archiveError(ctx, err)
		}
		readErr := entries.read(ctx, file.Name, file.Mode(), reader)
		closeErr := reader.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, archiveError(ctx, closeErr)
		}
	}
	return entries.finish()
}

func (u *Updater) extractTar(ctx context.Context, data []byte) ([]byte, error) {
	compressed, err := gzip.NewReader(contextReader{ctx, bytes.NewReader(data)})
	if err != nil {
		return nil, archiveError(ctx, err)
	}
	defer compressed.Close()
	expanded := &tarBudget{
		reader: contextReader{ctx, compressed}, remaining: u.limits.expanded,
	}
	entries := entries{u: u, seen: map[string]bool{}}
	for {
		var block [512]byte
		if _, err := io.ReadFull(expanded, block[:]); err != nil {
			return nil, archiveError(ctx, err)
		}
		if allZero(block[:]) {
			if _, err := io.ReadFull(expanded, block[:]); err != nil {
				return nil, archiveError(ctx, err)
			}
			if !allZero(block[:]) {
				return nil, errs.Security("release tar is missing its complete end marker")
			}
			break
		}
		// Inspect each physical header before tar.Reader can silently consume
		// PAX, GNU long-name, or sparse extension records.
		if block[156] != tar.TypeReg && block[156] != tar.TypeRegA {
			return nil, errs.Security("release tar contains a nonregular or extended entry")
		}
		archive := tar.NewReader(io.MultiReader(bytes.NewReader(block[:]), expanded))
		header, err := archive.Next()
		if err != nil {
			return nil, archiveError(ctx, err)
		}
		if (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) ||
			header.Linkname != "" || len(header.PAXRecords) != 0 ||
			header.Size < 0 || header.Size > u.limits.expanded {
			return nil, errs.Security("release tar contains a link, special, extended, or oversized entry")
		}
		if err := entries.read(ctx, header.Name, header.FileInfo().Mode(), archive); err != nil {
			return nil, err
		}
		padding := (512 - header.Size%512) % 512
		if _, err := io.ReadFull(expanded, block[:padding]); err != nil {
			return nil, archiveError(ctx, err)
		}
		if !allZero(block[:padding]) {
			return nil, errs.Security("release tar has nonzero entry padding")
		}
	}
	// Consume all gzip members and padding, validating CRCs and expansion limits.
	buffer := make([]byte, 32<<10)
	for {
		n, err := expanded.Read(buffer)
		if !allZero(buffer[:n]) {
			return nil, errs.Security("release tar contains trailing data")
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, archiveError(ctx, err)
		}
	}
	return entries.finish()
}

// Bound central-directory allocation before archive/zip parses it. ZIP64 and
// multi-disk archives are unnecessary for these sub-4GiB, few-file releases.
func validateZIPDirectory(data []byte, entryLimit int) error {
	invalid := func() error { return errs.Security("release ZIP has an invalid or excessive central directory") }
	end := -1
	for offset := len(data) - 22; offset >= 0 && offset >= len(data)-22-65535; offset-- {
		if binary.LittleEndian.Uint32(data[offset:]) != 0x06054b50 {
			continue
		}
		comment := int(binary.LittleEndian.Uint16(data[offset+20:]))
		if offset+22+comment > len(data) {
			continue
		}
		if offset+22+comment != len(data) {
			return invalid()
		}
		end = offset
		break
	}
	if end < 0 {
		return invalid()
	}
	record := data[end:]
	count := int(binary.LittleEndian.Uint16(record[10:]))
	size := uint64(binary.LittleEndian.Uint32(record[12:]))
	offset := uint64(binary.LittleEndian.Uint32(record[16:]))
	if binary.LittleEndian.Uint16(record[4:]) != 0 || binary.LittleEndian.Uint16(record[6:]) != 0 ||
		int(binary.LittleEndian.Uint16(record[8:])) != count || count > entryLimit ||
		offset+size != uint64(end) {
		return invalid()
	}
	seen := 0
	for offset < uint64(end) {
		if offset+46 > uint64(end) || seen >= count {
			return invalid()
		}
		header := data[offset:]
		if binary.LittleEndian.Uint32(header) != 0x02014b50 {
			return invalid()
		}
		length := uint64(binary.LittleEndian.Uint16(header[28:])) +
			uint64(binary.LittleEndian.Uint16(header[30:])) +
			uint64(binary.LittleEndian.Uint16(header[32:]))
		offset += 46 + length
		seen++
	}
	if offset != uint64(end) || seen != count {
		return invalid()
	}
	return nil
}

type tarBudget struct {
	reader    io.Reader
	remaining int64
}

func (r *tarBudget) Read(p []byte) (int, error) {
	if r.remaining < 0 {
		return 0, errs.Security("release tar exceeds its expansion limit")
	}
	if int64(len(p)) > r.remaining+1 {
		p = p[:r.remaining+1]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	if r.remaining < 0 {
		return n, errs.Security("release tar exceeds its expansion limit")
	}
	return n, err
}

func allZero(data []byte) bool {
	for _, b := range data {
		if b != 0 {
			return false
		}
	}
	return true
}
