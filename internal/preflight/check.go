// Package preflight checks that BARGE's Windows prerequisites are met before
// any container or image operation is attempted. It gives beginners clear,
// actionable error messages instead of cryptic API failures.
package preflight

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

const containerdPipe = `\\.\pipe\containerd-containerd`

// Check runs all prerequisite checks and returns the first error with a fix hint.
func Check() error {
	checks := []struct {
		name string
		fn   func() error
	}{
		{"admin privileges", checkAdmin},
		{"Hyper-V available", checkHyperV},
		{"containerd service", checkContainerdService},
		{"containerd reachable", checkContainerdPipe},
	}

	for _, c := range checks {
		if err := c.fn(); err != nil {
			return fmt.Errorf("prerequisite check failed [%s]: %w", c.name, err)
		}
	}
	return nil
}

// CheckAdmin returns an error if the current process is not running as Administrator.
func CheckAdmin() error { return checkAdmin() }

func checkAdmin() error {
	var sid *windows.SID
	err := windows.AllocateAndInitializeSid(
		&windows.SECURITY_NT_AUTHORITY,
		2,
		windows.SECURITY_BUILTIN_DOMAIN_RID,
		windows.DOMAIN_ALIAS_RID_ADMINS,
		0, 0, 0, 0, 0, 0,
		&sid,
	)
	if err != nil {
		return fmt.Errorf("could not check admin status: %w", err)
	}
	defer windows.FreeSid(sid)

	token := windows.Token(0)
	member, err := token.IsMember(sid)
	if err != nil {
		return fmt.Errorf("could not check token membership: %w", err)
	}
	if !member {
		return fmt.Errorf(
			"BARGE must run as Administrator\n\n" +
				"  Right-click your terminal and choose 'Run as administrator', then try again.",
		)
	}
	return nil
}

func checkHyperV() error {
	m, err := mgr.Connect()
	if err != nil {
		return nil // Can't check services — may still work
	}
	defer m.Disconnect()

	svc, err := m.OpenService("vmms")
	if err != nil {
		return fmt.Errorf(
			"Hyper-V is not enabled on this machine\n\n" +
				"  Enable it with (run as Administrator, then reboot):\n" +
				"  Enable-WindowsOptionalFeature -Online -FeatureName Microsoft-Hyper-V-All",
		)
	}
	svc.Close()
	return nil
}

func checkContainerdService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("cannot connect to Windows Service Manager: %w", err)
	}
	defer m.Disconnect()

	svc, err := m.OpenService("containerd")
	if err != nil {
		return fmt.Errorf(
			"containerd service not found\n\n" +
				"  Install containerd for Windows:\n" +
				"    1. Download: https://github.com/containerd/containerd/releases\n" +
				"    2. Extract to: C:\\Program Files\\containerd\\\n" +
				"    3. Register:  containerd.exe --register-service\n" +
				"    4. Start:     sc start containerd\n\n" +
				"  Also make sure the Windows Containers feature is enabled:\n" +
				"    Enable-WindowsOptionalFeature -Online -FeatureName Containers",
		)
	}
	defer svc.Close()

	status, err := svc.Query()
	if err != nil {
		return fmt.Errorf("cannot query containerd service: %w", err)
	}

	// svc_status.State 4 = Running
	if status.State != 4 {
		return fmt.Errorf(
			"containerd service is not running (state: %d)\n\n"+
				"  Start it with (run as Administrator):\n"+
				"    sc start containerd\n"+
				"  Or in PowerShell:\n"+
				"    Start-Service containerd",
			status.State,
		)
	}
	return nil
}

func checkContainerdPipe() error {
	// Open the named pipe using the Windows CreateFile API.
	// The standard net package does not support Windows named pipes.
	pipePath, err := windows.UTF16PtrFromString(containerdPipe)
	if err != nil {
		return fmt.Errorf("invalid pipe path: %w", err)
	}
	handle, err := windows.CreateFile(
		pipePath,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OVERLAPPED,
		0,
	)
	if err != nil {
		return fmt.Errorf(
			"cannot reach containerd at %s\n\n"+
				"  Ensure containerd is running: sc start containerd\n\n"+
				"  Original error: %w",
			containerdPipe, err,
		)
	}
	windows.CloseHandle(handle)
	return nil
}

// ContainerdAddress returns the named pipe address for containerd.
func ContainerdAddress() string {
	if addr := os.Getenv("BARGE_CONTAINERD_ADDR"); addr != "" {
		return addr
	}
	return containerdPipe
}
