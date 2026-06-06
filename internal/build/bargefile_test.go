package build

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
		check   func(*testing.T, *Bargefile)
	}{
		{
			name: "full valid bargefile",
			input: `# comment line
FROM mcr.microsoft.com/windows/servercore:ltsc2022
COPY . /app
RUN cmd.exe /c echo hello
ENV KEY=VALUE
CMD ["cmd.exe", "/c", "myapp.exe"]`,
			check: func(t *testing.T, bf *Bargefile) {
				if len(bf.Instructions) != 5 {
					t.Fatalf("want 5 instructions, got %d", len(bf.Instructions))
				}
				assertInstr(t, bf.Instructions[0], InstrFROM,
					[]string{"mcr.microsoft.com/windows/servercore:ltsc2022"})
				assertInstr(t, bf.Instructions[1], InstrCOPY, []string{".", "/app"})
				assertInstr(t, bf.Instructions[2], InstrRUN, []string{"cmd.exe /c echo hello"})
				assertInstr(t, bf.Instructions[3], InstrENV, []string{"KEY=VALUE"})
				assertInstr(t, bf.Instructions[4], InstrCMD, []string{"cmd.exe", "/c", "myapp.exe"})
			},
		},
		{
			name: "ENV space-separated",
			input: `FROM base:latest
ENV MY_KEY my value`,
			check: func(t *testing.T, bf *Bargefile) {
				got := bf.Instructions[1].Args[0]
				if got != "MY_KEY=my value" {
					t.Errorf("ENV: want %q, got %q", "MY_KEY=my value", got)
				}
			},
		},
		{
			name: "CMD plain text",
			input: `FROM base:latest
CMD myapp.exe --flag`,
			check: func(t *testing.T, bf *Bargefile) {
				assertInstr(t, bf.Instructions[1], InstrCMD, []string{"myapp.exe", "--flag"})
			},
		},
		{
			name: "blank lines and comments ignored",
			input: `
# build my app
FROM base:latest

# run it
RUN cmd.exe /c echo done
`,
			check: func(t *testing.T, bf *Bargefile) {
				if len(bf.Instructions) != 2 {
					t.Fatalf("want 2 instructions, got %d", len(bf.Instructions))
				}
			},
		},
		{
			name:    "missing FROM",
			input:   "RUN cmd.exe /c echo hello",
			wantErr: true,
		},
		{
			name:    "empty (only comments)",
			input:   "# just a comment\n",
			wantErr: true,
		},
		{
			name:    "unknown instruction",
			input:   "FROM base:latest\nADD src dst",
			wantErr: true,
		},
		{
			name:    "COPY wrong arg count — too few",
			input:   "FROM base:latest\nCOPY only-one",
			wantErr: true,
		},
		{
			name:    "COPY wrong arg count — too many",
			input:   "FROM base:latest\nCOPY a b c",
			wantErr: true,
		},
		{
			name:    "ENV no equals and no space",
			input:   "FROM base:latest\nENV BADKEY",
			wantErr: true,
		},
		{
			name:    "CMD invalid JSON array",
			input:   `FROM base:latest` + "\n" + `CMD ["unclosed"`,
			wantErr: true,
		},
		{
			name: "WORKDIR single arg",
			input: `FROM base:latest
WORKDIR /app`,
			check: func(t *testing.T, bf *Bargefile) {
				assertInstr(t, bf.Instructions[1], InstrWORKDIR, []string{"/app"})
			},
		},
		{
			name: "EXPOSE multiple ports",
			input: `FROM base:latest
EXPOSE 80 443 8080`,
			check: func(t *testing.T, bf *Bargefile) {
				assertInstr(t, bf.Instructions[1], InstrEXPOSE, []string{"80", "443", "8080"})
			},
		},
		{
			name: "ARG with default",
			input: `FROM base:latest
ARG VERSION=3.11`,
			check: func(t *testing.T, bf *Bargefile) {
				assertInstr(t, bf.Instructions[1], InstrARG, []string{"VERSION=3.11"})
			},
		},
		{
			name: "ARG without default",
			input: `FROM base:latest
ARG MYVAR`,
			check: func(t *testing.T, bf *Bargefile) {
				assertInstr(t, bf.Instructions[1], InstrARG, []string{"MYVAR"})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bf, err := Parse(strings.NewReader(tc.input))
			if tc.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.check != nil {
				tc.check(t, bf)
			}
		})
	}
}

func assertInstr(t *testing.T, got Instruction, wantType InstructionType, wantArgs []string) {
	t.Helper()
	if got.Type != wantType {
		t.Errorf("type: want %s, got %s", wantType, got.Type)
	}
	if len(got.Args) != len(wantArgs) {
		t.Errorf("args len: want %d, got %d (%v)", len(wantArgs), len(got.Args), got.Args)
		return
	}
	for i, want := range wantArgs {
		if got.Args[i] != want {
			t.Errorf("args[%d]: want %q, got %q", i, want, got.Args[i])
		}
	}
}
