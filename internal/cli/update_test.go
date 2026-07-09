package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCLIUpdateInstallsReleaseAsset(t *testing.T) {
	t.Parallel()

	tag := "v9.9.9"
	asset := updateAssetName(tag, runtime.GOOS, runtime.GOARCH)
	if asset == "" {
		t.Skipf("unsupported test platform %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	binaryContent := []byte("fake paxm " + tag)
	archiveBytes := testUpdateArchive(t, tag, binaryContent)
	checksum := sha256.Sum256(archiveBytes)
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(checksum[:]), asset)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/"+asset):
			_, _ = w.Write(archiveBytes)
		case strings.HasSuffix(r.URL.Path, "/SHA256SUMS"):
			_, _ = io.WriteString(w, checksums)
		default:
			t.Fatalf("unexpected update request: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	installPath := filepath.Join(t.TempDir(), "paxm")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Main([]string{
		"update",
		"--version", tag,
		"--repo", defaultUpdateRepo,
		"--base-url", server.URL,
		"--install-path", installPath,
	}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("update failed with code %d: %s", code, stderr.String())
	}
	installed, err := os.ReadFile(installPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(installed) != string(binaryContent) {
		t.Fatalf("unexpected installed content: %q", string(installed))
	}
	if !strings.Contains(stdout.String(), "updated paxm: dev -> "+tag) {
		t.Fatalf("unexpected update output: %s", stdout.String())
	}
}

func TestCLIUpdateCheckUsesLatestRelease(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/"+defaultUpdateRepo+"/releases/latest" {
			t.Fatalf("unexpected latest request: %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"tag_name":"v9.9.9"}`)
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Main([]string{
		"update",
		"--check",
		"--repo", defaultUpdateRepo,
		"--api-base-url", server.URL,
	}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("update check failed with code %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "update available: dev -> v9.9.9") {
		t.Fatalf("unexpected check output: %s", stdout.String())
	}
}

func TestUpdateChecksumMismatchFails(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archivePath := filepath.Join(dir, "paxm_v9.9.9_"+runtime.GOOS+"_"+runtime.GOARCH+".tar.gz")
	checksumPath := filepath.Join(dir, "SHA256SUMS")
	if err := os.WriteFile(archivePath, []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checksumPath, []byte(strings.Repeat("0", 64)+"  "+filepath.Base(archivePath)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksumFile(archivePath, checksumPath); err == nil {
		t.Fatal("expected checksum mismatch")
	}
}

func testUpdateArchive(t *testing.T, tag string, binaryContent []byte) []byte {
	t.Helper()

	assetDir := "paxm_" + tag + "_" + runtime.GOOS + "_" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		var buf bytes.Buffer
		writer := zip.NewWriter(&buf)
		file, err := writer.Create(assetDir + "/paxm.exe")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(binaryContent); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}

	var buf bytes.Buffer
	gzipWriter := gzip.NewWriter(&buf)
	tarWriter := tar.NewWriter(gzipWriter)
	header := &tar.Header{
		Name: assetDir + "/paxm",
		Mode: 0o755,
		Size: int64(len(binaryContent)),
	}
	if err := tarWriter.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(binaryContent); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
