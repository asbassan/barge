// Package setup installs and configures BARGE's Windows prerequisites:
// Hyper-V, the Windows Containers feature, and containerd.
package setup

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/svc/mgr"
)

const (
	containerdDir  = `C:\Program Files\containerd`
	containerdExe  = `C:\Program Files\containerd\containerd.exe`
	releaseAPIURL  = "https://api.github.com/repos/containerd/containerd/releases/latest"
	rebootExitCode = 3010
	rebootMsg      = "enabled — reboot required"
)

// RunInit runs all four setup steps sequentially. Steps that are already
// satisfied are skipped. If a Windows feature enable requires a reboot,
// setup stops and instructs the user to reboot before continuing.
func RunInit() error {
	fmt.Println()
	fmt.Println("  Setting up BARGE on this machine...")
	fmt.Println()

	rebootNeeded := false

	fmt.Println("  [1/4] Hyper-V ...")
	msg, err := stepHyperV()
	if err != nil {
		return err
	}
	fmt.Printf("        ✓ %s\n\n", msg)
	if msg == rebootMsg {
		rebootNeeded = true
	}

	fmt.Println("  [2/4] Windows Containers feature ...")
	msg, err = stepContainers()
	if err != nil {
		return err
	}
	fmt.Printf("        ✓ %s\n\n", msg)
	if msg == rebootMsg {
		rebootNeeded = true
	}

	if rebootNeeded {
		fmt.Println("  A reboot is required to continue setup.")
		fmt.Println("  Please reboot and run 'barge init' again.")
		fmt.Println()
		return nil
	}

	fmt.Println("  [3/4] containerd ...")
	msg, err = stepInstallContainerd()
	if err != nil {
		return err
	}
	fmt.Printf("        ✓ %s\n\n", msg)

	fmt.Println("  [4/4] containerd service ...")
	msg, err = stepStartContainerd()
	if err != nil {
		return err
	}
	fmt.Printf("        ✓ %s\n\n", msg)

	fmt.Println("  BARGE is ready.")
	fmt.Println("  Try: barge pull mcr.microsoft.com/windows/nanoserver:ltsc2022")
	fmt.Println()
	return nil
}

// stepHyperV ensures Hyper-V is enabled. Uses the vmms service as the
// indicator — if it's registered, Hyper-V is present.
func stepHyperV() (string, error) {
	m, err := mgr.Connect()
	if err == nil {
		defer m.Disconnect()
		svc, err := m.OpenService("vmms")
		if err == nil {
			svc.Close()
			return "already enabled", nil
		}
	}

	cmd := exec.Command("powershell", "-NonInteractive", "-Command",
		`$r = Enable-WindowsOptionalFeature -Online -FeatureName Microsoft-Hyper-V-All `+
			`-NoRestart -WarningAction SilentlyContinue; `+
			`if ($r.RestartNeeded) { exit 3010 }; exit 0`)
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == rebootExitCode {
			return rebootMsg, nil
		}
		return "", fmt.Errorf(
			"failed to enable Hyper-V\n\n"+
				"  On Windows 11 Home, Hyper-V is not available.\n"+
				"  BARGE requires Windows 11 Pro, Enterprise, or Education.\n\n"+
				"  Original error: %w", err)
	}
	return "enabled", nil
}

// stepContainers ensures the Windows Containers feature is enabled.
func stepContainers() (string, error) {
	out, err := exec.Command("powershell", "-NonInteractive", "-Command",
		`(Get-WindowsOptionalFeature -Online -FeatureName Containers).State`).Output()
	if err == nil && strings.TrimSpace(string(out)) == "Enabled" {
		return "already enabled", nil
	}

	cmd := exec.Command("powershell", "-NonInteractive", "-Command",
		`$r = Enable-WindowsOptionalFeature -Online -FeatureName Containers `+
			`-NoRestart -WarningAction SilentlyContinue; `+
			`if ($r.RestartNeeded) { exit 3010 }; exit 0`)
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == rebootExitCode {
			return rebootMsg, nil
		}
		return "", fmt.Errorf("failed to enable Windows Containers feature: %w", err)
	}
	return "enabled", nil
}

// stepInstallContainerd downloads and installs containerd if not already present.
func stepInstallContainerd() (string, error) {
	if _, err := os.Stat(containerdExe); err == nil {
		return "already installed", nil
	}

	fmt.Println("        Fetching latest containerd release ...")
	rel, err := latestContainerdRelease()
	if err != nil {
		return "", err
	}

	var downloadURL, assetName string
	for _, a := range rel.Assets {
		if strings.Contains(a.Name, "windows-amd64") && strings.HasSuffix(a.Name, ".tar.gz") {
			downloadURL = a.BrowserDownloadURL
			assetName = a.Name
			break
		}
	}
	if downloadURL == "" {
		return "", fmt.Errorf("no Windows AMD64 asset in containerd release %s", rel.TagName)
	}

	fmt.Printf("        Downloading %s ...\n", assetName)
	if err := os.MkdirAll(containerdDir, 0755); err != nil {
		return "", fmt.Errorf("cannot create %s: %w", containerdDir, err)
	}
	if err := downloadAndExtract(downloadURL, containerdDir); err != nil {
		return "", fmt.Errorf("download/extract failed: %w", err)
	}

	fmt.Println("        Registering containerd service ...")
	out, err := exec.Command(containerdExe, "--register-service").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("service registration failed: %w\n%s", err, out)
	}

	return fmt.Sprintf("%s installed", rel.TagName), nil
}

// stepStartContainerd starts the containerd Windows service.
func stepStartContainerd() (string, error) {
	m, err := mgr.Connect()
	if err != nil {
		return "", fmt.Errorf("cannot connect to service manager: %w", err)
	}
	defer m.Disconnect()

	svc, err := m.OpenService("containerd")
	if err != nil {
		return "", fmt.Errorf("containerd service not found — install step may have failed")
	}
	defer svc.Close()

	status, err := svc.Query()
	if err != nil {
		return "", fmt.Errorf("cannot query service status: %w", err)
	}
	if status.State == 4 { // SERVICE_RUNNING
		return "already running", nil
	}

	out, err := exec.Command("powershell", "-NonInteractive", "-Command",
		"Start-Service containerd").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("cannot start containerd: %w\n%s", err, out)
	}
	return "started", nil
}

type githubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func latestContainerdRelease() (*githubRelease, error) {
	req, err := http.NewRequest("GET", releaseAPIURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "barge/0.1")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach GitHub API: %w", err)
	}
	defer resp.Body.Close()

	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("cannot parse GitHub API response: %w", err)
	}
	if rel.TagName == "" {
		return nil, fmt.Errorf("unexpected empty response from GitHub API")
	}
	return &rel, nil
}

// downloadAndExtract streams a .tar.gz from url and extracts its bin/ contents
// into destDir. Files are written as destDir/<filename> (the bin/ prefix is stripped).
func downloadAndExtract(url, destDir string) error {
	resp, err := http.Get(url) //nolint:noctx
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	gr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("cannot read gzip stream: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read error: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		// Strip any leading directory (e.g. "bin/containerd.exe" → "containerd.exe").
		name := filepath.Base(hdr.Name)
		dst := filepath.Join(destDir, name)

		f, err := os.Create(dst)
		if err != nil {
			return fmt.Errorf("cannot create %s: %w", dst, err)
		}
		_, copyErr := io.Copy(f, tr)
		f.Close()
		if copyErr != nil {
			return fmt.Errorf("cannot write %s: %w", dst, copyErr)
		}
	}
	return nil
}
