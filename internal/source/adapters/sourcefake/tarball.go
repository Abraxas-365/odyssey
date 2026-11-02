package sourcefake

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// tarTime keeps the fake's tarballs reproducible.
var tarTime = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// buildTarball is a gzipped tarball shaped like GitHub's: a pax global
// header carrying the commit, then one top folder "owner-repo-<sha7>/" with
// the files below it.
func buildTarball(fullName, commit string, files map[string]string) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if err := writePaxGlobalHeader(gz, commit); err != nil {
		return nil, err
	}
	tw := tar.NewWriter(gz)

	short := commit
	if len(short) > 7 {
		short = short[:7]
	}
	top := strings.ReplaceAll(fullName, "/", "-") + "-" + short + "/"
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: top, Mode: 0o755, ModTime: tarTime}); err != nil {
		return nil, err
	}

	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	made := map[string]bool{}
	for _, p := range paths {
		p = strings.TrimPrefix(p, "/")
		// Parent folders first, as git archive writes them.
		parts := strings.Split(p, "/")
		for i := 1; i < len(parts); i++ {
			dir := top + strings.Join(parts[:i], "/") + "/"
			if made[dir] {
				continue
			}
			made[dir] = true
			if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: dir, Mode: 0o755, ModTime: tarTime}); err != nil {
				return nil, err
			}
		}
	}
	for _, orig := range paths {
		content := files[orig]
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     top + strings.TrimPrefix(orig, "/"),
			Mode:     0o644,
			Size:     int64(len(content)),
			ModTime:  tarTime,
		}); err != nil {
			return nil, err
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writePaxGlobalHeader writes the entry git archive starts with, by hand:
// archive/tar can write a global header but names it GlobalHead.0.0, while
// git (and GitHub) call it pax_global_header.
func writePaxGlobalHeader(w io.Writer, commit string) error {
	rec := " comment=" + commit + "\n"
	// A pax record is "<length> <key>=<value>\n" where length counts itself.
	n := len(rec) + 1
	for len(strconv.Itoa(n))+len(rec) != n {
		n = len(strconv.Itoa(n)) + len(rec)
	}
	data := strconv.Itoa(n) + rec

	var h [512]byte
	copy(h[0:], "pax_global_header")
	copy(h[100:], "0000666\x00")
	copy(h[108:], "0000000\x00")
	copy(h[116:], "0000000\x00")
	copy(h[124:], fmt.Sprintf("%011o\x00", len(data)))
	copy(h[136:], fmt.Sprintf("%011o\x00", tarTime.Unix()))
	copy(h[148:], "        ") // checksum placeholder: spaces
	h[156] = 'g'
	copy(h[257:], "ustar\x0000")
	sum := 0
	for _, b := range h {
		sum += int(b)
	}
	copy(h[148:], fmt.Sprintf("%06o\x00 ", sum))
	if _, err := w.Write(h[:]); err != nil {
		return err
	}
	if _, err := io.WriteString(w, data); err != nil {
		return err
	}
	if pad := 512 - len(data)%512; pad != 512 {
		_, err := w.Write(make([]byte, pad))
		return err
	}
	return nil
}
