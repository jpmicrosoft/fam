package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func response(status int, data []byte) *http.Response {
	return &http.Response{
		StatusCode: status, Header: make(http.Header),
		Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data)),
	}
}

func testUpdater(t *testing.T, current, wanted, goos string) *Updater {
	t.Helper()
	path := filepath.Join(t.TempDir(), executableName(goos))
	mustWrite(t, path, []byte("original executable fixture"), 0755)
	return updaterAt(t, path, current, wanted, goos)
}

func updaterAt(t *testing.T, path, current, wanted, goos string) *Updater {
	t.Helper()
	options, installed, selected, err := validateOptions(Options{
		CurrentVersion: current, Version: wanted,
	})
	if err != nil {
		t.Fatal(err)
	}
	u, err := newUpdater(options, installed, selected, path, goos, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	// Fail closed if a test forgets to install its synthetic transport.
	u.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("offline test has no synthetic response")
	})
	return u
}

func mustWrite(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func directoryNames(t *testing.T, path string) []string {
	t.Helper()
	items, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Name())
	}
	return names
}

type fixture struct {
	release  release
	metadata []byte
	archive  []byte
	checksum []byte
	mu       sync.Mutex
	paths    []string
}

func attachFixture(t *testing.T, u *Updater, target string, executable []byte) *fixture {
	t.Helper()
	stable := false
	name := "fam_" + target + archiveSuffix(u.goos, u.arch)
	f := &fixture{
		release: release{
			TagName: "v" + target, Draft: &stable, Prerelease: &stable,
			Assets: []asset{{ID: 11, Name: name}, {ID: 22, Name: "SHA256SUMS"}},
		},
		archive: makeArchive(t, u.goos, standardEntries(u.goos, executable)),
	}
	f.checksum = checksumFixture(f.archive, name)
	u.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.paths = append(f.paths, request.URL.Path)
		switch request.URL.String() {
		case releaseEndpoint + "latest", releaseEndpoint + "tags/v" + target:
			data := f.metadata
			if data == nil {
				var err error
				data, err = json.Marshal(f.release)
				if err != nil {
					return nil, err
				}
			}
			return response(200, data), nil
		case assetEndpoint(11), assetEndpoint(33):
			return response(200, f.archive), nil
		case assetEndpoint(22):
			return response(200, f.checksum), nil
		default:
			return nil, fmt.Errorf("unexpected offline request")
		}
	})
	return f
}

func checksumFixture(archive []byte, name string) []byte {
	return []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(archive), name))
}

func (f *fixture) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

type archiveEntry struct {
	name     string
	data     []byte
	mode     os.FileMode
	typeflag byte
	link     string
}

func standardEntries(goos string, executable []byte) []archiveEntry {
	return []archiveEntry{
		{name: executableName(goos), data: executable, mode: 0755},
		{name: "LICENSE", data: []byte("offline license fixture"), mode: 0644},
		{name: "THIRD_PARTY_NOTICES.txt", data: []byte("offline notices fixture"), mode: 0644},
	}
}

func makeArchive(t *testing.T, goos string, entries []archiveEntry) []byte {
	t.Helper()
	if goos == "windows" {
		return makeZIP(t, entries)
	}
	return gzipBytes(t, tarBytes(t, entries))
}

func makeZIP(t *testing.T, entries []archiveEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Store}
		header.SetMode(entry.mode)
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func tarBytes(t *testing.T, entries []archiveEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, entry := range entries {
		header := &tar.Header{
			Name: entry.name, Mode: int64(entry.mode.Perm()), Size: int64(len(entry.data)),
			Typeflag: entry.typeflag, Linkname: entry.link,
		}
		if entry.mode&os.ModeSetuid != 0 {
			header.Mode |= 04000
		}
		if entry.typeflag == tar.TypeSymlink || entry.typeflag == tar.TypeLink ||
			entry.typeflag == tar.TypeDir || entry.typeflag == tar.TypeFifo {
			header.Size = 0
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size != 0 {
			if _, err := writer.Write(entry.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
