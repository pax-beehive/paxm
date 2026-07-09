package cli

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/pax-beehive/memory-adaptor/internal/config"
)

const defaultUpdateRepo = "pax-beehive/memory-adaptor"

type updateOptions struct {
	repo        string
	version     string
	check       bool
	force       bool
	installPath string
	baseURL     string
	apiBaseURL  string
}

type updateRelease struct {
	TagName string `json:"tag_name"`
}

func (r runner) runUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	options := updateOptions{}
	fs.StringVar(&options.repo, "repo", defaultUpdateRepo, "GitHub repository owner/name")
	fs.StringVar(&options.version, "version", "", "release version to install")
	fs.BoolVar(&options.check, "check", false, "check for an available update without installing")
	fs.BoolVar(&options.force, "force", false, "install even when the requested version matches the current version")
	fs.StringVar(&options.installPath, "install-path", "", "path to install paxm")
	fs.StringVar(&options.baseURL, "base-url", "https://github.com", "GitHub base URL")
	fs.StringVar(&options.apiBaseURL, "api-base-url", "https://api.github.com", "GitHub API base URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	updater := newUpdater(options, r.stdout)
	return updater.Run(context.Background())
}

type updater struct {
	options updateOptions
	client  *http.Client
	stdout  io.Writer
}

func newUpdater(options updateOptions, stdout io.Writer) updater {
	if stdout == nil {
		stdout = io.Discard
	}
	if options.repo == "" {
		options.repo = defaultUpdateRepo
	}
	if options.baseURL == "" {
		options.baseURL = "https://github.com"
	}
	if options.apiBaseURL == "" {
		options.apiBaseURL = "https://api.github.com"
	}
	return updater{
		options: options,
		client:  &http.Client{Timeout: 2 * time.Minute},
		stdout:  stdout,
	}
}

func (u updater) Run(ctx context.Context) error {
	tag, err := u.resolveVersion(ctx)
	if err != nil {
		return err
	}
	if u.options.check {
		if version == tag {
			fmt.Fprintf(u.stdout, "paxm is up to date: %s\n", version)
			return nil
		}
		fmt.Fprintf(u.stdout, "update available: %s -> %s\n", version, tag)
		return nil
	}
	if version == tag && !u.options.force {
		fmt.Fprintf(u.stdout, "paxm is already at %s\n", tag)
		return nil
	}

	asset := updateAssetName(tag, runtime.GOOS, runtime.GOARCH)
	if asset == "" {
		return fmt.Errorf("unsupported platform: %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	tempDir, err := os.MkdirTemp("", "paxm-update-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)

	archivePath := filepath.Join(tempDir, asset)
	if err := u.download(ctx, u.releaseDownloadURL(tag, asset), archivePath); err != nil {
		return err
	}
	checksumsPath := filepath.Join(tempDir, "SHA256SUMS")
	if err := u.download(ctx, u.releaseDownloadURL(tag, "SHA256SUMS"), checksumsPath); err != nil {
		return err
	}
	if err := verifyChecksumFile(archivePath, checksumsPath); err != nil {
		return err
	}

	extracted, err := extractPaxmBinary(archivePath, tempDir)
	if err != nil {
		return err
	}
	installPath, err := u.installPath()
	if err != nil {
		return err
	}
	if err := installBinary(extracted, installPath); err != nil {
		return err
	}
	fmt.Fprintf(u.stdout, "updated paxm: %s -> %s\n", version, tag)
	fmt.Fprintf(u.stdout, "installed: %s\n", installPath)
	return nil
}

func (u updater) resolveVersion(ctx context.Context) (string, error) {
	if strings.TrimSpace(u.options.version) != "" {
		return normalizeReleaseTag(u.options.version), nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(u.options.apiBaseURL, "/")+"/repos/"+u.options.repo+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	addGitHubHeaders(request)
	response, err := u.client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("fetch latest release: %s", response.Status)
	}
	var release updateRelease
	if err := json.NewDecoder(response.Body).Decode(&release); err != nil {
		return "", err
	}
	if strings.TrimSpace(release.TagName) == "" {
		return "", errors.New("latest release response did not include tag_name")
	}
	return release.TagName, nil
}

func (u updater) download(ctx context.Context, url, path string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	addGitHubHeaders(request)
	response, err := u.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("download %s: %s", url, response.Status)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, response.Body)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func (u updater) releaseDownloadURL(tag, asset string) string {
	return strings.TrimRight(u.options.baseURL, "/") + "/" + u.options.repo + "/releases/download/" + tag + "/" + asset
}

func (u updater) installPath() (string, error) {
	if strings.TrimSpace(u.options.installPath) != "" {
		return filepath.Abs(config.ExpandPath(u.options.installPath))
	}
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(path)
}

func addGitHubHeaders(request *http.Request) {
	request.Header.Set("User-Agent", "paxm/"+version)
	request.Header.Set("Accept", "application/vnd.github+json")
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
}

func normalizeReleaseTag(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "v") {
		return value
	}
	return "v" + value
}

func updateAssetName(tag, goos, goarch string) string {
	base := "paxm_" + tag + "_" + goos + "_" + goarch
	if goos == "windows" {
		return base + ".zip"
	}
	switch goos {
	case "darwin", "linux":
		return base + ".tar.gz"
	default:
		return ""
	}
}

func verifyChecksumFile(archivePath, checksumsPath string) error {
	expected, err := expectedChecksum(filepath.Base(archivePath), checksumsPath)
	if err != nil {
		return err
	}
	actual, err := fileSHA256(archivePath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(expected, actual) {
		return fmt.Errorf("checksum mismatch for %s", filepath.Base(archivePath))
	}
	return nil
}

func expectedChecksum(asset, checksumsPath string) (string, error) {
	bytes, err := os.ReadFile(checksumsPath)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(bytes), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == asset {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("checksum for %s was not found", asset)
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func extractPaxmBinary(archivePath, tempDir string) (string, error) {
	if strings.HasSuffix(archivePath, ".zip") {
		return extractPaxmFromZip(archivePath, tempDir)
	}
	return extractPaxmFromTarGz(archivePath, tempDir)
}

func extractPaxmFromTarGz(archivePath, tempDir string) (string, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return "", err
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		if header.FileInfo().IsDir() || filepath.Base(header.Name) != "paxm" {
			continue
		}
		return writeExtractedBinary(reader, tempDir, "paxm")
	}
	return "", fmt.Errorf("paxm binary not found in %s", filepath.Base(archivePath))
}

func extractPaxmFromZip(archivePath, tempDir string) (string, error) {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", err
	}
	defer reader.Close()
	for _, file := range reader.File {
		if file.FileInfo().IsDir() || filepath.Base(file.Name) != "paxm.exe" {
			continue
		}
		body, err := file.Open()
		if err != nil {
			return "", err
		}
		defer body.Close()
		return writeExtractedBinary(body, tempDir, "paxm.exe")
	}
	return "", fmt.Errorf("paxm.exe binary not found in %s", filepath.Base(archivePath))
}

func writeExtractedBinary(reader io.Reader, tempDir, name string) (string, error) {
	path := filepath.Join(tempDir, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(file, reader)
	closeErr := file.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return path, nil
}

func installBinary(source, target string) error {
	target = config.ExpandPath(target)
	if runtime.GOOS == "windows" {
		if current, err := os.Executable(); err == nil && samePath(current, target) {
			return errors.New("self-update cannot replace a running Windows executable; pass --install-path to install elsewhere")
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(target), ".paxm-update-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	sourceFile, err := os.Open(source)
	if err != nil {
		_ = temp.Close()
		_ = os.Remove(tempPath)
		return err
	}
	_, copyErr := io.Copy(temp, sourceFile)
	closeSourceErr := sourceFile.Close()
	closeTempErr := temp.Close()
	if copyErr != nil {
		_ = os.Remove(tempPath)
		return copyErr
	}
	if closeSourceErr != nil {
		_ = os.Remove(tempPath)
		return closeSourceErr
	}
	if closeTempErr != nil {
		_ = os.Remove(tempPath)
		return closeTempErr
	}
	if err := os.Chmod(tempPath, 0o755); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if runtime.GOOS == "windows" {
		if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(tempPath)
			return err
		}
	}
	return os.Rename(tempPath, target)
}

func samePath(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return left == right
	}
	return strings.EqualFold(leftAbs, rightAbs)
}
