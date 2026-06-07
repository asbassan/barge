// Package dockerfile parses Dockerfiles and converts them to Bargefile
// instructions. Unsupported instructions (ENTRYPOINT, LABEL, USER, etc.)
// are skipped and returned as warnings so the user knows what was dropped.
package dockerfile

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/asbassan/barge/internal/build"
)

// Parse reads a Dockerfile and converts it to a *build.Bargefile.
// The second return value is a list of human-readable warnings for
// instructions that were skipped or converted with caveats.
func Parse(r io.Reader) (*build.Bargefile, []string, error) {
	lines, err := readLines(r)
	if err != nil {
		return nil, nil, err
	}

	var warnings []string
	var entrypoint []string
	bf := &build.Bargefile{}

	for _, line := range lines {
		parts := strings.SplitN(line, " ", 2)
		instr := strings.ToUpper(strings.TrimSpace(parts[0]))
		rest := ""
		if len(parts) == 2 {
			rest = strings.TrimSpace(parts[1])
		}

		switch instr {
		case "FROM":
			// Strip --platform=... flags and AS <alias> (multi-stage).
			ref := stripFromModifiers(rest)
			bf.Instructions = append(bf.Instructions, build.Instruction{
				Type: build.InstrFROM,
				Args: []string{ref},
			})

		case "ENV":
			// Docker ENV supports two forms:
			//   KEY=VALUE [KEY2=VALUE2 ...]   (new form, one or more pairs)
			//   KEY VALUE                      (deprecated single-var form)
			for _, kv := range normalizeEnv(rest) {
				bf.Instructions = append(bf.Instructions, build.Instruction{
					Type: build.InstrENV,
					Args: []string{kv},
				})
			}

		case "WORKDIR":
			bf.Instructions = append(bf.Instructions, build.Instruction{
				Type: build.InstrWORKDIR,
				Args: []string{rest},
			})

		case "COPY":
			// Skip --from=<stage> (multi-stage copy).
			if strings.HasPrefix(rest, "--") {
				flag := strings.Fields(rest)[0]
				warnings = append(warnings, fmt.Sprintf(
					"COPY %s: multi-stage flag not supported in Barge — skipped", flag))
				continue
			}
			instrs, warn := parseCopy(rest)
			warnings = append(warnings, warn...)
			bf.Instructions = append(bf.Instructions, instrs...)

		case "ADD":
			// ADD is COPY + URL support + tar auto-extraction. Map to COPY
			// and warn that URL sources and tar extraction are not supported.
			instrs, warn := parseCopy(rest)
			warnings = append(warnings, warn...)
			warnings = append(warnings,
				"ADD mapped to COPY — URL sources and automatic tar extraction are not supported")
			bf.Instructions = append(bf.Instructions, instrs...)

		case "RUN":
			bf.Instructions = append(bf.Instructions, build.Instruction{
				Type: build.InstrRUN,
				Args: []string{rest},
			})

		case "CMD":
			args, err := parseShellOrExec(rest)
			if err != nil {
				return nil, warnings, fmt.Errorf("CMD: %w", err)
			}
			bf.Instructions = append(bf.Instructions, build.Instruction{
				Type: build.InstrCMD,
				Args: args,
			})

		case "ENTRYPOINT":
			args, err := parseShellOrExec(rest)
			if err != nil {
				return nil, warnings, fmt.Errorf("ENTRYPOINT: %w", err)
			}
			entrypoint = args
			warnings = append(warnings,
				"ENTRYPOINT is not supported in Barge — merged with CMD (ENTRYPOINT args prepended to CMD args)")

		case "EXPOSE":
			bf.Instructions = append(bf.Instructions, build.Instruction{
				Type: build.InstrEXPOSE,
				Args: strings.Fields(rest),
			})

		case "ARG":
			bf.Instructions = append(bf.Instructions, build.Instruction{
				Type: build.InstrARG,
				Args: []string{rest},
			})

		case "LABEL", "USER", "VOLUME", "HEALTHCHECK", "SHELL",
			"ONBUILD", "STOPSIGNAL", "MAINTAINER":
			warnings = append(warnings,
				fmt.Sprintf("%s is not supported in Barge — skipped", instr))

		default:
			warnings = append(warnings,
				fmt.Sprintf("unknown instruction %q — skipped", instr))
		}
	}

	// Merge ENTRYPOINT into CMD: prepend entrypoint args to the last CMD.
	if len(entrypoint) > 0 {
		merged := false
		for i := len(bf.Instructions) - 1; i >= 0; i-- {
			if bf.Instructions[i].Type == build.InstrCMD {
				bf.Instructions[i].Args = append(entrypoint, bf.Instructions[i].Args...)
				merged = true
				break
			}
		}
		if !merged {
			// No CMD present — use ENTRYPOINT as CMD.
			bf.Instructions = append(bf.Instructions, build.Instruction{
				Type: build.InstrCMD,
				Args: entrypoint,
			})
		}
	}

	if len(bf.Instructions) == 0 || bf.Instructions[0].Type != build.InstrFROM {
		return nil, warnings, fmt.Errorf("Dockerfile must start with a FROM instruction")
	}

	return bf, warnings, nil
}

// readLines reads the file joining backslash-continued lines into single
// logical lines, and stripping blank lines and comments.
func readLines(r io.Reader) ([]string, error) {
	scanner := bufio.NewScanner(r)
	var lines []string
	var buf strings.Builder

	for scanner.Scan() {
		trimmed := strings.TrimSpace(scanner.Text())

		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		if strings.HasSuffix(trimmed, "\\") {
			// Line continuation: strip the backslash and accumulate.
			buf.WriteString(strings.TrimSuffix(trimmed, "\\"))
			buf.WriteString(" ")
			continue
		}

		buf.WriteString(trimmed)
		lines = append(lines, strings.TrimSpace(buf.String()))
		buf.Reset()
	}

	if buf.Len() > 0 {
		lines = append(lines, strings.TrimSpace(buf.String()))
	}

	return lines, scanner.Err()
}

// stripFromModifiers removes --platform=... flags and AS <alias> from a FROM
// argument, returning just the image reference.
// "python:3.11-slim"                          → "python:3.11-slim"
// "--platform=linux/amd64 ubuntu:22.04 AS b"  → "ubuntu:22.04"
func stripFromModifiers(rest string) string {
	fields := strings.Fields(rest)
	var result []string
	for i := 0; i < len(fields); i++ {
		if strings.HasPrefix(fields[i], "--") {
			continue
		}
		if strings.ToUpper(fields[i]) == "AS" {
			break
		}
		result = append(result, fields[i])
	}
	if len(result) == 0 {
		return rest
	}
	return strings.Join(result, " ")
}

// normalizeEnv converts Docker's ENV forms to a slice of "KEY=VALUE" strings.
func normalizeEnv(rest string) []string {
	// New form with = signs: KEY=VALUE [KEY2=VALUE2 ...]
	if strings.Contains(rest, "=") {
		// Split on word boundaries between KEY=VALUE pairs.
		// Pattern: a space followed by a word that contains "=".
		// Simple heuristic: split on spaces, re-join runs without "=".
		fields := strings.Fields(rest)
		var pairs []string
		var current string
		for _, f := range fields {
			if strings.Contains(f, "=") {
				if current != "" {
					pairs = append(pairs, current)
				}
				current = f
			} else if current != "" {
				// Value with spaces (e.g. ENV KEY=hello world → KEY=hello world)
				current += " " + f
			}
		}
		if current != "" {
			pairs = append(pairs, current)
		}
		if len(pairs) > 0 {
			return pairs
		}
	}
	// Deprecated form: KEY VALUE
	kv := strings.SplitN(rest, " ", 2)
	if len(kv) == 2 {
		return []string{kv[0] + "=" + strings.TrimSpace(kv[1])}
	}
	return []string{rest}
}

// parseCopy handles "src [src2 ...] dst" and expands multi-source COPY
// into multiple single-source instructions.
func parseCopy(rest string) ([]build.Instruction, []string) {
	fields := strings.Fields(rest)
	if len(fields) < 2 {
		return nil, []string{fmt.Sprintf("COPY %q: needs at least src and dst — skipped", rest)}
	}

	dst := fields[len(fields)-1]
	sources := fields[:len(fields)-1]

	var instrs []build.Instruction
	var warnings []string

	if len(sources) > 1 {
		warnings = append(warnings, fmt.Sprintf(
			"COPY with %d sources expanded into %d separate COPY instructions",
			len(sources), len(sources)))
	}

	for _, src := range sources {
		instrs = append(instrs, build.Instruction{
			Type: build.InstrCOPY,
			Args: []string{src, dst},
		})
	}
	return instrs, warnings
}

// parseShellOrExec parses a CMD or ENTRYPOINT value in either exec form
// (["cmd","arg"]) or shell form (plain string split into words).
func parseShellOrExec(rest string) ([]string, error) {
	if strings.HasPrefix(rest, "[") {
		var args []string
		if err := json.Unmarshal([]byte(rest), &args); err != nil {
			return nil, fmt.Errorf("invalid JSON exec form %q: %w", rest, err)
		}
		return args, nil
	}
	return strings.Fields(rest), nil
}
