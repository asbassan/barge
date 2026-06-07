package dockerfile

import (
	"fmt"
	"strings"

	"github.com/asbassan/barge/internal/build"
)

// ConvertResult holds the Windows-converted bargefile and a log of every
// change or decision made during conversion.
type ConvertResult struct {
	Bargefile    *build.Bargefile
	Warnings     []string
	ImageChanged bool   // true when FROM was replaced with a Windows image
	OriginalFrom string // the original Linux image reference
	WindowsFrom  string // the replacement Windows image reference
}

// Convert takes a parsed Bargefile (typically from a Linux Dockerfile) and
// rewrites it to be compatible with Windows containers. winVersion is the
// Windows release tag suffix, e.g. "ltsc2022".
func Convert(bf *build.Bargefile, winVersion string) (*ConvertResult, error) {
	if winVersion == "" {
		winVersion = "ltsc2022"
	}

	result := &ConvertResult{
		Bargefile: &build.Bargefile{},
	}

	for _, instr := range bf.Instructions {
		switch instr.Type {

		case build.InstrFROM:
			original := instr.Args[0]
			winImage, changed := toWindowsImage(original, winVersion)
			if changed {
				result.ImageChanged = true
				result.OriginalFrom = original
				result.WindowsFrom = winImage
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("FROM: replaced Linux image %q with Windows image %q", original, winImage))
			}
			result.Bargefile.Instructions = append(result.Bargefile.Instructions,
				build.Instruction{Type: build.InstrFROM, Args: []string{winImage}})

		case build.InstrWORKDIR:
			winPath := toWindowsPath(instr.Args[0])
			if winPath != instr.Args[0] {
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("WORKDIR: %q → %q", instr.Args[0], winPath))
			}
			result.Bargefile.Instructions = append(result.Bargefile.Instructions,
				build.Instruction{Type: build.InstrWORKDIR, Args: []string{winPath}})

		case build.InstrRUN:
			converted, warns, skip := convertRun(instr.Args[0])
			result.Warnings = append(result.Warnings, warns...)
			if !skip {
				result.Bargefile.Instructions = append(result.Bargefile.Instructions,
					build.Instruction{Type: build.InstrRUN, Args: []string{converted}})
			}

		case build.InstrCOPY:
			// COPY destinations that are absolute Linux paths need converting.
			src := instr.Args[0]
			dst := instr.Args[1]
			winDst := toWindowsPath(dst)
			result.Bargefile.Instructions = append(result.Bargefile.Instructions,
				build.Instruction{Type: build.InstrCOPY, Args: []string{src, winDst}})

		default:
			// ENV, CMD, EXPOSE, ARG — pass through unchanged.
			result.Bargefile.Instructions = append(result.Bargefile.Instructions, instr)
		}
	}

	return result, nil
}

// FormatBargefile renders a Bargefile as text so it can be written to disk
// or printed for user inspection.
func FormatBargefile(bf *build.Bargefile) string {
	var sb strings.Builder
	for _, instr := range bf.Instructions {
		switch instr.Type {
		case build.InstrCMD:
			// Emit in JSON exec form.
			parts := make([]string, len(instr.Args))
			for i, a := range instr.Args {
				parts[i] = fmt.Sprintf("%q", a)
			}
			fmt.Fprintf(&sb, "CMD [%s]\n", strings.Join(parts, ", "))
		case build.InstrEXPOSE:
			fmt.Fprintf(&sb, "EXPOSE %s\n", strings.Join(instr.Args, " "))
		case build.InstrCOPY:
			fmt.Fprintf(&sb, "COPY %s %s\n", instr.Args[0], instr.Args[1])
		default:
			fmt.Fprintf(&sb, "%s %s\n", instr.Type, strings.Join(instr.Args, " "))
		}
	}
	return sb.String()
}

// ─── image mapping ────────────────────────────────────────────────────────────

// linuxVariantSuffixes are tag segments that signal a Linux-only variant.
var linuxVariantSuffixes = []string{
	"-slim", "-alpine", "-bookworm", "-bullseye", "-buster", "-stretch",
	"-focal", "-jammy", "-noble", "-centos", "-ubi", "-bionic",
}

// windowsKeywords: if any appear in the image ref it is already Windows.
var windowsKeywords = []string{
	"windowsservercore", "nanoserver", "ltsc", "windows-cssc",
}

// runtimeWindowsTags maps a runtime image name to a function that produces
// the Windows tag given the version string and target winVersion.
var runtimeWindowsTags = map[string]func(ver, winVer string) string{
	"python": func(ver, wv string) string {
		return fmt.Sprintf("python:%s-windowsservercore-%s", ver, wv)
	},
	"node": func(ver, wv string) string {
		return fmt.Sprintf("node:%s-windowsservercore-%s", ver, wv)
	},
	"nodejs": func(ver, wv string) string {
		return fmt.Sprintf("node:%s-windowsservercore-%s", ver, wv)
	},
	"golang": func(ver, wv string) string {
		return fmt.Sprintf("golang:%s-windowsservercore-%s", ver, wv)
	},
	"go": func(ver, wv string) string {
		return fmt.Sprintf("golang:%s-windowsservercore-%s", ver, wv)
	},
	"ruby": func(ver, wv string) string {
		return fmt.Sprintf("ruby:%s-windowsservercore-%s", ver, wv)
	},
	"dotnet": func(ver, wv string) string {
		return fmt.Sprintf("mcr.microsoft.com/dotnet/runtime:%s-windowsservercore-%s", ver, wv)
	},
}

// genericLinuxImages have no runtime-specific Windows equivalent;
// replace with the base Windows Server Core image.
var genericLinuxImages = map[string]bool{
	"ubuntu": true, "debian": true, "alpine": true,
	"centos": true, "fedora": true, "rhel": true,
	"amazonlinux": true, "oraclelinux": true, "opensuse": true,
	"scratch": true, "busybox": true,
}

// toWindowsImage converts a Linux image reference to its Windows equivalent.
// Returns the new reference and whether a change was made.
func toWindowsImage(ref, winVersion string) (string, bool) {
	// Already a Windows image?
	lower := strings.ToLower(ref)
	for _, kw := range windowsKeywords {
		if strings.Contains(lower, kw) {
			return ref, false
		}
	}
	if strings.HasPrefix(lower, "mcr.microsoft.com/windows") {
		return ref, false
	}

	// Parse "name:tag" — handle registries like "docker.io/library/python:3.11-slim"
	name, tag := splitImageRef(ref)
	baseName := baseImageName(name) // "python", "node", etc.
	ver := stripLinuxVariant(tag)   // "3.11-slim" → "3.11"

	// Runtime with known Windows tag?
	if fn, ok := runtimeWindowsTags[baseName]; ok {
		return fn(ver, winVersion), true
	}

	// Generic Linux distro → bare Windows Server Core.
	if genericLinuxImages[baseName] {
		return fmt.Sprintf("mcr.microsoft.com/windows/servercore:%s", winVersion), true
	}

	// Unknown image — leave as-is, warn caller via the unchanged flag.
	return ref, false
}

// splitImageRef returns (name, tag) for "name:tag" or "registry/name:tag".
func splitImageRef(ref string) (string, string) {
	tag := "latest"
	if idx := strings.LastIndex(ref, ":"); idx > strings.LastIndex(ref, "/") {
		tag = ref[idx+1:]
		ref = ref[:idx]
	}
	return ref, tag
}

// baseImageName extracts the short image name from a full reference path.
// "docker.io/library/python" → "python", "python" → "python"
func baseImageName(name string) string {
	parts := strings.Split(name, "/")
	base := strings.ToLower(parts[len(parts)-1])
	// Strip registry prefix artifacts like "library"
	return base
}

// stripLinuxVariant removes Linux-specific tag suffixes.
// "3.11-slim" → "3.11", "3.11-alpine" → "3.11", "3.11" → "3.11", "latest" → "latest"
func stripLinuxVariant(tag string) string {
	for _, suffix := range linuxVariantSuffixes {
		if strings.Contains(tag, suffix) {
			return tag[:strings.Index(tag, suffix)]
		}
	}
	return tag
}

// ─── RUN conversion ───────────────────────────────────────────────────────────

// convertRun converts a Linux shell RUN command to its Windows equivalent.
// Returns: converted command, warnings, and whether the instruction should be skipped entirely.
func convertRun(cmd string) (string, []string, bool) {
	// Split on " && " to process each sub-command independently.
	parts := strings.Split(cmd, " && ")
	var converted []string
	var warnings []string

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		newCmd, warn, skip := convertSingleCommand(part)
		if warn != "" {
			warnings = append(warnings, "RUN: "+warn)
		}
		if skip {
			continue
		}
		converted = append(converted, newCmd)
	}

	if len(converted) == 0 {
		return "", warnings, true
	}
	return strings.Join(converted, " && "), warnings, false
}

// convertSingleCommand converts one shell command (no && chains).
func convertSingleCommand(cmd string) (newCmd, warning string, skip bool) {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return "", "", true
	}

	exe := fields[0]
	args := fields[1:]

	switch exe {
	case "mkdir":
		return convertMkdir(args), "", false

	case "chmod", "chown", "chgrp":
		// No-op on Windows — silently skip.
		return "", fmt.Sprintf("%s is a no-op on Windows — skipped", exe), true

	case "apt-get", "apt":
		return "", "apt-get is not available on Windows — remove or replace with a Windows installer", true

	case "apk":
		return "", "apk (Alpine package manager) is not available on Windows — remove or replace with a Windows installer", true

	case "yum", "dnf", "zypper", "pacman":
		return "", fmt.Sprintf("%s is not available on Windows — remove or replace with a Windows installer", exe), true

	case "curl":
		return convertCurl(args), "", false

	case "wget":
		return convertWget(args), "", false

	case "ln":
		return convertLn(args), "", false

	case "rm":
		return convertRm(args), "", false

	case "cp":
		return convertCp(args), "", false

	case "touch":
		return convertTouch(args), "", false

	case "export":
		// export KEY=VALUE in a RUN is meaningless in containers anyway.
		return "", "export is a no-op in container RUN instructions — use ENV instead", true

	case "useradd", "adduser", "groupadd":
		return "", fmt.Sprintf("%s is not available on Windows — USER instruction also not supported", exe), true

	default:
		// Unknown command — pass through and let the container runtime report any failure.
		return cmd, "", false
	}
}

func convertMkdir(args []string) string {
	var paths []string
	skipNext := false
	for _, a := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if a == "-p" {
			continue
		}
		if a == "-m" { // mkdir -m 755 /path
			skipNext = true
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		paths = append(paths, toWindowsPath(a))
	}
	if len(paths) == 0 {
		return ""
	}
	return fmt.Sprintf(
		`powershell -Command New-Item -ItemType Directory -Force %s`,
		strings.Join(paths, ", "))
}

func convertCurl(args []string) string {
	// curl -o file url  →  Invoke-WebRequest -Uri url -OutFile file
	// curl -fsSL url    →  Invoke-WebRequest -Uri url -UseBasicParsing
	var url, outFile string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-o", "--output":
			if i+1 < len(args) {
				outFile = args[i+1]
				i++
			}
		case "-L", "-f", "-s", "-S", "--silent", "--fail", "--location":
			// common curl flags — drop them
		default:
			if !strings.HasPrefix(args[i], "-") {
				url = args[i]
			}
		}
	}
	if url == "" {
		return `powershell -Command Invoke-WebRequest ` + strings.Join(args, " ")
	}
	if outFile != "" {
		return fmt.Sprintf(
			`powershell -Command Invoke-WebRequest -Uri '%s' -OutFile '%s' -UseBasicParsing`,
			url, outFile)
	}
	return fmt.Sprintf(
		`powershell -Command Invoke-WebRequest -Uri '%s' -UseBasicParsing`,
		url)
}

func convertWget(args []string) string {
	// wget -O file url  →  Invoke-WebRequest -Uri url -OutFile file
	var url, outFile string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-O", "--output-document":
			if i+1 < len(args) {
				outFile = args[i+1]
				i++
			}
		case "-q", "--quiet":
			// drop
		default:
			if !strings.HasPrefix(args[i], "-") {
				url = args[i]
			}
		}
	}
	if url == "" {
		return `powershell -Command Invoke-WebRequest ` + strings.Join(args, " ")
	}
	if outFile != "" {
		return fmt.Sprintf(
			`powershell -Command Invoke-WebRequest -Uri '%s' -OutFile '%s' -UseBasicParsing`,
			url, outFile)
	}
	return fmt.Sprintf(
		`powershell -Command Invoke-WebRequest -Uri '%s' -UseBasicParsing`,
		url)
}

func convertLn(args []string) string {
	// ln -s src dst  →  New-Item -ItemType SymbolicLink -Path dst -Target src
	var src, dst string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			continue
		}
		if src == "" {
			src = toWindowsPath(args[i])
		} else {
			dst = toWindowsPath(args[i])
		}
	}
	if src == "" || dst == "" {
		return `powershell -Command New-Item -ItemType SymbolicLink ` + strings.Join(args, " ")
	}
	return fmt.Sprintf(
		`powershell -Command New-Item -ItemType SymbolicLink -Path '%s' -Target '%s'`,
		dst, src)
}

func convertRm(args []string) string {
	// rm -rf path  →  Remove-Item -Recurse -Force path
	var paths []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		paths = append(paths, toWindowsPath(a))
	}
	if len(paths) == 0 {
		return ""
	}
	return fmt.Sprintf(
		`powershell -Command Remove-Item -Recurse -Force %s`,
		strings.Join(paths, ", "))
}

func convertCp(args []string) string {
	// cp src dst  →  Copy-Item -Recurse src dst
	var paths []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		paths = append(paths, toWindowsPath(a))
	}
	if len(paths) < 2 {
		return `powershell -Command Copy-Item -Recurse ` + strings.Join(args, " ")
	}
	return fmt.Sprintf(
		`powershell -Command Copy-Item -Recurse '%s' '%s'`,
		paths[0], paths[len(paths)-1])
}

func convertTouch(args []string) string {
	// touch file  →  New-Item -Force file
	var paths []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			paths = append(paths, toWindowsPath(a))
		}
	}
	if len(paths) == 0 {
		return ""
	}
	return fmt.Sprintf(
		`powershell -Command New-Item -Force %s`,
		strings.Join(paths, ", "))
}

// toWindowsPath converts a Unix absolute path to a Windows path.
// Relative paths and already-Windows paths are returned unchanged.
func toWindowsPath(p string) string {
	if !strings.HasPrefix(p, "/") {
		return p
	}
	return "C:" + strings.ReplaceAll(p, "/", `\`)
}
