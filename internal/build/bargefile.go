// Package build implements the barge build workflow: parse a Bargefile and
// execute its instructions to produce a new container image.
package build

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// InstructionType is one of the Bargefile directives.
type InstructionType string

const (
	InstrFROM    InstructionType = "FROM"
	InstrCOPY    InstructionType = "COPY"
	InstrRUN     InstructionType = "RUN"
	InstrENV     InstructionType = "ENV"
	InstrCMD     InstructionType = "CMD"
	InstrWORKDIR InstructionType = "WORKDIR"
	InstrEXPOSE  InstructionType = "EXPOSE"
	InstrARG     InstructionType = "ARG"
)

// Instruction is a single parsed line from a Bargefile.
type Instruction struct {
	Type InstructionType
	Args []string
}

// Bargefile holds the ordered list of instructions.
type Bargefile struct {
	Instructions []Instruction
}

// Parse reads a Bargefile from r and returns the parsed instructions.
// The first instruction must be FROM.
func Parse(r io.Reader) (*Bargefile, error) {
	bf := &Bargefile{}
	scanner := bufio.NewScanner(r)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, " ", 2)
		if len(parts) < 2 {
			return nil, fmt.Errorf("line %d: %s requires arguments", lineNum, parts[0])
		}
		rest := strings.TrimSpace(parts[1])

		instrType := InstructionType(strings.ToUpper(parts[0]))
		var args []string

		switch instrType {
		case InstrFROM:
			args = []string{rest}

		case InstrCOPY:
			fields := strings.Fields(rest)
			if len(fields) != 2 {
				return nil, fmt.Errorf("line %d: COPY requires exactly two arguments: src dest", lineNum)
			}
			args = fields

		case InstrRUN:
			args = []string{rest}

		case InstrENV:
			if strings.Contains(rest, "=") {
				args = []string{rest}
			} else {
				kv := strings.SplitN(rest, " ", 2)
				if len(kv) != 2 {
					return nil, fmt.Errorf("line %d: ENV requires KEY=VALUE or KEY VALUE", lineNum)
				}
				args = []string{kv[0] + "=" + strings.TrimSpace(kv[1])}
			}

		case InstrCMD:
			if strings.HasPrefix(rest, "[") {
				if err := json.Unmarshal([]byte(rest), &args); err != nil {
					return nil, fmt.Errorf("line %d: CMD JSON: %w", lineNum, err)
				}
			} else {
				args = strings.Fields(rest)
			}

		case InstrWORKDIR:
			args = []string{rest}

		case InstrEXPOSE:
			args = strings.Fields(rest)

		case InstrARG:
			args = []string{rest}

		default:
			return nil, fmt.Errorf("line %d: unknown instruction %q", lineNum, parts[0])
		}

		bf.Instructions = append(bf.Instructions, Instruction{Type: instrType, Args: args})
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading Bargefile: %w", err)
	}

	if len(bf.Instructions) == 0 || bf.Instructions[0].Type != InstrFROM {
		return nil, fmt.Errorf("Bargefile must start with a FROM instruction")
	}

	return bf, nil
}
