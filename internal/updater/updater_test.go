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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		v1, v2 string
		want   int
	}{
		{"1.2.4", "1.2.4", 0},
		{"v1.2.4", "1.2.4", 0},
		{"1.2.4", "v1.2.4", 0},
		{"v1.2.3", "v1.2.4", -1},
		{"v1.2.5", "v1.2.4", 1},
		{"1.10.0", "1.9.9", 1},
		{"1.2.0", "1.2", 0},
		{"1.2.4-beta", "1.2.4", 0},
		{"dev", "1.0.0", -1},
		{"1.0.0", "dev", 1},
		{"dev", "dev", 0},
	}

	for _, tt := range tests {
		got := CompareVersions(tt.v1, tt.v2)
		if got != tt.want {
			t.Errorf("CompareVersions(%q, %q) = %d; want %d", tt.v1, tt.v2, got, tt.want)
		}
	}
}

func TestIsNewer(t *testing.T) {
	if !IsNewer("v1.2.5", "v1.2.4") {
		t.Errorf("expected v1.2.5 to be newer than v1.2.4")
	}
	if IsNewer("v1.2.4", "v1.2.4") {
		t.Errorf("expected v1.2.4 not to be newer than v1.2.4")
	}
	if IsNewer("v1.2.3", "v1.2.4") {
		t.Errorf("expected v1.2.3 not to be newer than v1.2.4")
	}
}

func TestFindAsset(t *testing.T) {
	assets := []Asset{
		{Name: "harderstyles-radar_v1.2.4_darwin_arm64.tar.gz"},
		{Name: "harderstyles-radar_v1.2.4_linux_amd64.tar.gz"},
		{Name: "harderstyles-radar_v1.2.4_windows_amd64.zip"},
		{Name: "checksums.txt"},
	}

	a, err := FindAsset(assets, "darwin", "arm64")
	if err != nil || a == nil || a.Name != "harderstyles-radar_v1.2.4_darwin_arm64.tar.gz" {
		t.Fatalf("unexpected asset: %v, err: %v", a, err)
	}

	w, err := FindAsset(assets, "windows", "amd64")
	if err != nil || w == nil || w.Name != "harderstyles-radar_v1.2.4_windows_amd64.zip" {
		t.Fatalf("unexpected asset: %v, err: %v", w, err)
	}

	_, err = FindAsset(assets, "freebsd", "386")
	if err == nil {
		t.Errorf("expected error for unsupported platform")
	}
}

func TestVerifyChecksum(t *testing.T) {
	data := []byte("binary payload content")
	sum := sha256.Sum256(data)
	hashStr := hex.EncodeToString(sum[:])

	checksums := fmt.Sprintf("%s  harderstyles-radar_v1.2.5_darwin_arm64.tar.gz\n%s  other.tar.gz\n", hashStr, "abcd")

	if err := VerifyChecksum(data, checksums, "harderstyles-radar_v1.2.5_darwin_arm64.tar.gz"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if err := VerifyChecksum([]byte("wrong data"), checksums, "harderstyles-radar_v1.2.5_darwin_arm64.tar.gz"); err == nil {
		t.Errorf("expected checksum mismatch error")
	}

	if err := VerifyChecksum(data, checksums, "nonexistent.tar.gz"); err == nil {
		t.Errorf("expected missing checksum error")
	}
}

func TestExtractBinaryTarGz(t *testing.T) {
	expectedPayload := []byte("#!/bin/sh\necho hello\n")

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	hdr := &tar.Header{
		Name: "harderstyles-radar",
		Mode: 0755,
		Size: int64(len(expectedPayload)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(expectedPayload); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}

	extracted, err := ExtractBinary(buf.Bytes(), "harderstyles-radar_v1.2.5_linux_amd64.tar.gz", "linux")
	if err != nil {
		t.Fatalf("extract failed: %v", err)
	}
	if !bytes.Equal(extracted, expectedPayload) {
		t.Errorf("extracted payload mismatch: got %q, want %q", extracted, expectedPayload)
	}
}

func TestExtractBinaryZip(t *testing.T) {
	expectedPayload := []byte("windows-exe-binary-data")

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	f, err := zw.Create("harderstyles-radar.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(expectedPayload); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	extracted, err := ExtractBinary(buf.Bytes(), "harderstyles-radar_v1.2.5_windows_amd64.zip", "windows")
	if err != nil {
		t.Fatalf("extract failed: %v", err)
	}
	if !bytes.Equal(extracted, expectedPayload) {
		t.Errorf("extracted payload mismatch: got %q, want %q", extracted, expectedPayload)
	}
}

func TestReplaceExecutable(t *testing.T) {
	tmpDir := t.TempDir()
	exePath := filepath.Join(tmpDir, "test-binary")
	if err := os.WriteFile(exePath, []byte("v1"), 0755); err != nil {
		t.Fatal(err)
	}

	newBinary := []byte("v2-new-content")
	if err := ReplaceExecutable(exePath, newBinary); err != nil {
		t.Fatalf("ReplaceExecutable failed: %v", err)
	}

	content, err := os.ReadFile(exePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, newBinary) {
		t.Errorf("got %q, want %q", content, newBinary)
	}
}

func TestCheckAndApplyUpdateWithMockServer(t *testing.T) {
	targetExt := ".tar.gz"
	binName := "harderstyles-radar"
	if runtime.GOOS == "windows" {
		targetExt = ".zip"
		binName = "harderstyles-radar.exe"
	}
	assetName := fmt.Sprintf("harderstyles-radar_v2.0.0_%s_%s%s", runtime.GOOS, runtime.GOARCH, targetExt)

	// Create test archive
	newPayload := []byte("updated-binary-content-v2.0.0")
	var archiveBytes []byte

	if runtime.GOOS == "windows" {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		f, err := zw.Create(binName)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(newPayload); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		archiveBytes = buf.Bytes()
	} else {
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gw)
		hdr := &tar.Header{
			Name: binName,
			Mode: 0755,
			Size: int64(len(newPayload)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(newPayload); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gw.Close(); err != nil {
			t.Fatal(err)
		}
		archiveBytes = buf.Bytes()
	}

	sum := sha256.Sum256(archiveBytes)
	checksumsContent := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), assetName)

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/test/repo/releases/latest":
			rel := Release{
				TagName: "v2.0.0",
				Name:    "v2.0.0",
				Assets: []Asset{
					{
						Name:               assetName,
						BrowserDownloadURL: srv.URL + "/download/" + assetName,
					},
					{
						Name:               "checksums.txt",
						BrowserDownloadURL: srv.URL + "/download/checksums.txt",
					},
				},
			}
			_ = json.NewEncoder(w).Encode(rel)
		case "/download/" + assetName:
			_, _ = w.Write(archiveBytes)
		case "/download/checksums.txt":
			_, _ = w.Write([]byte(checksumsContent))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	hc := srv.Client()

	check, err := CheckURL(ctx, hc, srv.URL+"/repos/test/repo/releases/latest", "1.2.4")
	if err != nil {
		t.Fatalf("CheckURL failed: %v", err)
	}

	if !check.UpdateAvailable {
		t.Errorf("expected update to be available")
	}
	if check.LatestVersion != "v2.0.0" {
		t.Errorf("got latest version %q, want v2.0.0", check.LatestVersion)
	}
	if check.Asset == nil || check.Asset.Name != assetName {
		t.Errorf("unexpected asset: %v", check.Asset)
	}

	tmpDir := t.TempDir()
	targetBinary := filepath.Join(tmpDir, binName)
	if err := os.WriteFile(targetBinary, []byte("initial-binary"), 0755); err != nil {
		t.Fatal(err)
	}

	var statusLog []string
	err = ApplyUpdate(ctx, hc, targetBinary, check, func(s string) {
		statusLog = append(statusLog, s)
	})
	if err != nil {
		t.Fatalf("ApplyUpdate failed: %v", err)
	}

	updatedContent, err := os.ReadFile(targetBinary)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(updatedContent, newPayload) {
		t.Errorf("updated binary content mismatch: got %q, want %q", updatedContent, newPayload)
	}
	if len(statusLog) == 0 {
		t.Errorf("expected status updates during apply")
	}
}
