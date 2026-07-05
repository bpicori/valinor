package deploy

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

var versionPattern = regexp.MustCompile(`^version=(v\S+)`)

type release struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"assets"`
}

func latestRelease(ctx context.Context, repo string) (tag, assetName string, assetID int, err error) {
	var rel release
	if err := githubGET(ctx, fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo), &rel); err != nil {
		return "", "", 0, err
	}

	tag = strings.TrimSpace(rel.TagName)
	if tag == "" {
		return "", "", 0, fmt.Errorf("latest release tag is empty")
	}

	assetName = releaseAssetName(tag)
	for _, asset := range rel.Assets {
		if asset.Name == assetName {
			return tag, assetName, asset.ID, nil
		}
	}
	return "", "", 0, fmt.Errorf("release asset not found: %s", assetName)
}

func releaseAssetName(tag string) string {
	version := strings.TrimPrefix(tag, "v")
	return fmt.Sprintf("valinor_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
}

func githubGET(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("github request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read github response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("github request failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode github response: %w", err)
	}
	return nil
}

func downloadReleaseAsset(ctx context.Context, repo string, assetID int, assetName, dest string) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/assets/%d", repo, assetID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create download request: %w", err)
	}
	req.Header.Set("Accept", "application/octet-stream")

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", assetName, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("download %s failed: %s", assetName, strings.TrimSpace(string(body)))
	}

	file, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("create archive file: %w", err)
	}
	defer file.Close()

	if _, err := io.Copy(file, resp.Body); err != nil {
		return fmt.Errorf("write archive %s: %w", dest, err)
	}
	return nil
}

func binaryVersion(binary string) (string, error) {
	if strings.TrimSpace(binary) == "" {
		return "", fmt.Errorf("binary path is empty")
	}

	out, err := exec.Command(binary, "version").CombinedOutput()
	if err != nil {
		return "", nil
	}

	for _, line := range strings.Split(string(out), "\n") {
		if matches := versionPattern.FindStringSubmatch(strings.TrimSpace(line)); len(matches) == 2 {
			return matches[1], nil
		}
	}
	return "", nil
}

func updateRelease(ctx context.Context, paths Paths, restart bool) error {
	latestTag, assetName, assetID, err := latestRelease(ctx, DefaultRepo)
	if err != nil {
		return err
	}

	current, err := binaryVersion(paths.Binary)
	if err != nil {
		return err
	}
	if current == latestTag {
		fmt.Printf("already up to date (%s)\n", latestTag)
		return nil
	}

	fmt.Printf("updating %s -> %s (%s)\n", displayVersion(current), latestTag, assetName)

	tmpDir, err := os.MkdirTemp("", "valinor-update-*")
	if err != nil {
		return fmt.Errorf("create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	archivePath := filepath.Join(tmpDir, assetName)
	if err := downloadReleaseAsset(ctx, DefaultRepo, assetID, assetName, archivePath); err != nil {
		return err
	}

	newBinary, err := extractBinary(archivePath, tmpDir)
	if err != nil {
		return err
	}

	newVersion, err := binaryVersion(newBinary)
	if err != nil {
		return err
	}
	if newVersion != latestTag {
		return fmt.Errorf("downloaded binary version mismatch: expected %s, got %s", latestTag, displayVersion(newVersion))
	}

	if err := installBinary(paths, newBinary); err != nil {
		return err
	}

	fmt.Printf("installed %s to %s\n", latestTag, paths.Binary)

	if restart {
		if err := platformRestartIfInstalled(ctx, paths); err != nil {
			return fmt.Errorf("binary updated but service restart failed: %w", err)
		}
	}

	return nil
}

func rollbackRelease(ctx context.Context, paths Paths, restart bool) error {
	if _, err := os.Stat(paths.PrevBinary); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no previous binary at %s", paths.PrevBinary)
		}
		return fmt.Errorf("stat previous binary: %w", err)
	}

	if err := copyFile(paths.PrevBinary, paths.Binary); err != nil {
		return fmt.Errorf("restore previous binary: %w", err)
	}
	if err := os.Chmod(paths.Binary, 0755); err != nil {
		return fmt.Errorf("chmod restored binary: %w", err)
	}

	fmt.Printf("restored previous binary to %s\n", paths.Binary)

	if restart {
		if err := platformRestartIfInstalled(ctx, paths); err != nil {
			return fmt.Errorf("restored binary but restart failed: %w", err)
		}
	}
	return nil
}

func displayVersion(version string) string {
	if strings.TrimSpace(version) == "" {
		return "<none>"
	}
	return version
}

func extractBinary(archivePath, destDir string) (string, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return "", fmt.Errorf("open archive: %w", err)
	}
	defer file.Close()

	gz, err := gzip.NewReader(file)
	if err != nil {
		return "", fmt.Errorf("read gzip archive: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read tar archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != BinaryName {
			continue
		}

		extracted := filepath.Join(destDir, BinaryName)
		out, err := os.OpenFile(extracted, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
		if err != nil {
			return "", fmt.Errorf("create extracted binary: %w", err)
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return "", fmt.Errorf("extract binary: %w", err)
		}
		if err := out.Close(); err != nil {
			return "", fmt.Errorf("close extracted binary: %w", err)
		}
		if err := os.Chmod(extracted, 0755); err != nil {
			return "", fmt.Errorf("chmod extracted binary: %w", err)
		}
		return extracted, nil
	}
	return "", fmt.Errorf("archive did not contain %s binary", BinaryName)
}

func installBinary(paths Paths, newBinary string) error {
	if err := os.MkdirAll(filepath.Dir(paths.Binary), 0755); err != nil {
		return fmt.Errorf("create binary directory: %w", err)
	}

	if _, err := os.Stat(paths.Binary); err == nil {
		if err := copyFile(paths.Binary, paths.PrevBinary); err != nil {
			return fmt.Errorf("backup current binary: %w", err)
		}
	}

	staged := paths.Binary + ".new"
	if err := copyFile(newBinary, staged); err != nil {
		return err
	}
	if err := os.Chmod(staged, 0755); err != nil {
		return fmt.Errorf("chmod staged binary: %w", err)
	}
	if err := os.Rename(staged, paths.Binary); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy %s to %s: %w", src, dst, err)
	}
	return nil
}