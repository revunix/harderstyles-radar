package updater

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"hardstylesync/internal/i18n"
)

// DefaultRepo is the GitHub repository holding releases.
const DefaultRepo = "revunix/harderstyles-radar"

// Release represents a release on GitHub.
type Release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []Asset   `json:"assets"`
}

// Asset represents a release asset on GitHub.
type Asset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// CheckResult holds the outcome of checking for updates.
type CheckResult struct {
	CurrentVersion  string
	LatestVersion   string
	UpdateAvailable bool
	Release         *Release
	Asset           *Asset
	ChecksumAsset   *Asset
}

// GetRepo returns the repository to check, honoring HARDERSTYLES_REPO if set.
func GetRepo() string {
	if r := strings.TrimSpace(os.Getenv("HARDERSTYLES_REPO")); r != "" {
		return r
	}
	return DefaultRepo
}

// Check queries GitHub for the latest release and compares it with currentVersion.
func Check(ctx context.Context, hc *http.Client, currentVersion string) (*CheckResult, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	repo := GetRepo()
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	return CheckURL(ctx, hc, url, currentVersion)
}

// CheckURL queries a custom release URL (useful for testing or proxying) and compares with currentVersion.
func CheckURL(ctx context.Context, hc *http.Client, url string, currentVersion string) (*CheckResult, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	userAgent := "harderstyles-radar"
	if currentVersion != "" {
		userAgent += "/" + currentVersion
	}
	req.Header.Set("User-Agent", userAgent)

	token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("GH_TOKEN"))
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.New(i18n.T("keine Releases gefunden", "no releases found"))
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return nil, errors.New(i18n.T("GitHub-API-Ratenlimit erreicht; bitte später erneut versuchen", "GitHub API rate limit exceeded; please try again later"))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api returned status %s", resp.Status)
	}

	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("parse release: %w", err)
	}

	latestVersion := rel.TagName
	updateAvailable := IsNewer(latestVersion, currentVersion)
	asset, _ := FindAsset(rel.Assets, runtime.GOOS, runtime.GOARCH)
	checksumAsset := FindChecksumAsset(rel.Assets)

	return &CheckResult{
		CurrentVersion:  currentVersion,
		LatestVersion:   latestVersion,
		UpdateAvailable: updateAvailable,
		Release:         &rel,
		Asset:           asset,
		ChecksumAsset:   checksumAsset,
	}, nil
}

// CompareVersions compares two version strings (e.g. "1.2.4" and "v1.2.5").
// Returns:
//   -1 if v1 < v2
//    0 if v1 == v2
//   +1 if v1 > v2
func CompareVersions(v1, v2 string) int {
	norm1 := normalizeVersion(v1)
	norm2 := normalizeVersion(v2)

	if norm1 == norm2 {
		return 0
	}
	if norm1 == "dev" {
		return -1
	}
	if norm2 == "dev" {
		return 1
	}

	nums1 := parseVersionNumbers(norm1)
	nums2 := parseVersionNumbers(norm2)

	maxLen := max(len(nums1), len(nums2))
	for i := 0; i < maxLen; i++ {
		n1 := 0
		if i < len(nums1) {
			n1 = nums1[i]
		}
		n2 := 0
		if i < len(nums2) {
			n2 = nums2[i]
		}
		if n1 < n2 {
			return -1
		}
		if n1 > n2 {
			return 1
		}
	}
	return 0
}

// IsNewer reports whether latest is newer than current.
func IsNewer(latest, current string) bool {
	return CompareVersions(latest, current) > 0
}

func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")
	return v
}

func parseVersionNumbers(v string) []int {
	// Strip any prerelease or build metadata, e.g. "1.2.4-beta" -> "1.2.4"
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	nums := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			break
		}
		nums = append(nums, n)
	}
	return nums
}

// FindAsset finds the matching asset for the given OS and architecture.
func FindAsset(assets []Asset, goos, goarch string) (*Asset, error) {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	targetSuffix := fmt.Sprintf("_%s_%s%s", goos, goarch, ext)
	for i := range assets {
		a := &assets[i]
		if strings.HasSuffix(a.Name, targetSuffix) {
			return a, nil
		}
	}
	return nil, fmt.Errorf("%s %s/%s", i18n.T("kein passendes Release-Paket gefunden für", "no release package found for"), goos, goarch)
}

// FindChecksumAsset finds the checksums.txt asset if present.
func FindChecksumAsset(assets []Asset) *Asset {
	for i := range assets {
		if assets[i].Name == "checksums.txt" {
			return &assets[i]
		}
	}
	return nil
}

// VerifyChecksum checks that data matches the sha256 checksum recorded for filename in checksumsContent.
func VerifyChecksum(data []byte, checksumsContent string, filename string) error {
	sum := sha256.Sum256(data)
	actual := hex.EncodeToString(sum[:])

	for _, line := range strings.Split(checksumsContent, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			expectedHash := strings.ToLower(fields[0])
			name := filepath.Base(fields[1])
			if name == filename {
				if !strings.EqualFold(actual, expectedHash) {
					return fmt.Errorf("%s %s: got %s, want %s", i18n.T("Prüfsummenfehler für", "checksum mismatch for"), filename, actual, expectedHash)
				}
				return nil
			}
		}
	}
	return fmt.Errorf("%s %s", i18n.T("keine Prüfsumme in checksums.txt gefunden für", "no checksum found in checksums.txt for"), filename)
}

// ExtractBinary unpacks the executable binary from the given archive data.
func ExtractBinary(archiveData []byte, filename string, goos string) ([]byte, error) {
	binaryName := "harderstyles-radar"
	if goos == "windows" {
		binaryName = "harderstyles-radar.exe"
	}

	if strings.HasSuffix(filename, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(archiveData), int64(len(archiveData)))
		if err != nil {
			return nil, fmt.Errorf("read zip: %w", err)
		}
		for _, f := range zr.File {
			if filepath.Base(f.Name) == binaryName {
				rc, err := f.Open()
				if err != nil {
					return nil, fmt.Errorf("open file in zip: %w", err)
				}
				defer rc.Close()
				data, err := io.ReadAll(rc)
				if err != nil {
					return nil, fmt.Errorf("read file in zip: %w", err)
				}
				return data, nil
			}
		}
		return nil, fmt.Errorf("%s: %s", binaryName, i18n.T("nicht im Zip-Archiv gefunden", "not found in zip archive"))
	}

	// Assume tar.gz
	gz, err := gzip.NewReader(bytes.NewReader(archiveData))
	if err != nil {
		return nil, fmt.Errorf("read gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar: %w", err)
		}
		if filepath.Base(hdr.Name) == binaryName {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("read file in tar: %w", err)
			}
			return data, nil
		}
	}
	return nil, fmt.Errorf("%s: %s", binaryName, i18n.T("nicht im Tar-Archiv gefunden", "not found in tar archive"))
}

// ReplaceExecutable safely replaces targetPath with newBinary across platforms.
func ReplaceExecutable(targetPath string, newBinary []byte) error {
	targetPath, err := filepath.EvalSymlinks(targetPath)
	if err != nil {
		return fmt.Errorf("resolve executable symlink: %w", err)
	}
	dir := filepath.Dir(targetPath)

	if runtime.GOOS == "windows" {
		oldPath := targetPath + ".old"
		_ = os.Remove(oldPath)
		if err := os.Rename(targetPath, oldPath); err != nil {
			return fmt.Errorf("%s: %w", i18n.T("kann bestehende Datei nicht umbenennen (fehlen Schreibrechte?)", "could not rename existing binary (missing write permissions?)"), err)
		}
		if err := os.WriteFile(targetPath, newBinary, 0755); err != nil {
			// Roll back
			_ = os.Rename(oldPath, targetPath)
			return fmt.Errorf("%s: %w", i18n.T("kann neue Datei nicht schreiben", "could not write new binary"), err)
		}
		_ = os.Remove(oldPath)
		return nil
	}

	tmpFile, err := os.CreateTemp(dir, ".harderstyles-radar-update-*")
	if err != nil {
		return fmt.Errorf("%s %s: %w", i18n.T("kann temporäre Datei nicht erstellen in", "could not create temp file in"), dir, err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		if _, err := os.Stat(tmpPath); err == nil {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmpFile.Write(newBinary); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("write temp binary: %w", err)
	}
	if err := tmpFile.Chmod(0755); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("chmod temp binary: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp binary: %w", err)
	}

	if err := os.Rename(tmpPath, targetPath); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("kann Binärdatei nicht ersetzen (fehlen Schreibrechte?)", "could not replace binary (missing write permissions?)"), err)
	}
	return nil
}

// ApplyUpdate downloads the release asset, verifies its checksum if available, extracts the binary and replaces targetPath.
func ApplyUpdate(ctx context.Context, hc *http.Client, targetPath string, check *CheckResult, status func(string)) error {
	if check == nil || check.Asset == nil {
		return errors.New(i18n.T("kein Download-Paket für die Aktualisierung verfügbar", "no download package available for update"))
	}
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	if targetPath == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("determine executable path: %w", err)
		}
		targetPath = exe
	}

	if status != nil {
		status(fmt.Sprintf("%s %s …", i18n.T("Lade", "Downloading"), check.Asset.Name))
	}

	archiveData, err := download(ctx, hc, check.Asset.BrowserDownloadURL)
	if err != nil {
		return fmt.Errorf("download archive: %w", err)
	}

	if check.ChecksumAsset != nil {
		if status != nil {
			status(i18n.T("Verifiziere Prüfsumme …", "Verifying checksum …"))
		}
		checksumData, err := download(ctx, hc, check.ChecksumAsset.BrowserDownloadURL)
		if err != nil {
			return fmt.Errorf("download checksums: %w", err)
		}
		if err := VerifyChecksum(archiveData, string(checksumData), check.Asset.Name); err != nil {
			return err
		}
	}

	if status != nil {
		status(i18n.T("Entpacke Update …", "Extracting update …"))
	}
	newBinary, err := ExtractBinary(archiveData, check.Asset.Name, runtime.GOOS)
	if err != nil {
		return err
	}

	if status != nil {
		status(i18n.T("Installiere neues Release …", "Installing new release …"))
	}
	if err := ReplaceExecutable(targetPath, newBinary); err != nil {
		return err
	}

	if status != nil {
		status(fmt.Sprintf("%s %s!", i18n.T("Erfolgreich aktualisiert auf", "Successfully updated to"), check.LatestVersion))
	}
	return nil
}

func download(ctx context.Context, hc *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("GH_TOKEN"))
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}
