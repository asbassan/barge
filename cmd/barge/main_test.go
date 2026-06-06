package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadEnvFile(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []string
		wantErr bool
	}{
		{
			name:    "basic key-value pairs",
			content: "KEY1=value1\nKEY2=value2\n",
			want:    []string{"KEY1=value1", "KEY2=value2"},
		},
		{
			name:    "comments and blank lines skipped",
			content: "# this is a comment\n\nKEY=value\n# another comment\n",
			want:    []string{"KEY=value"},
		},
		{
			name:    "value with equals sign",
			content: "URL=http://host:8080/path?a=b\n",
			want:    []string{"URL=http://host:8080/path?a=b"},
		},
		{
			name:    "empty file is valid",
			content: "# only comments\n\n",
			want:    nil,
		},
		{
			name:    "missing equals sign",
			content: "BADKEY\n",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := filepath.Join(t.TempDir(), ".env")
			if err := os.WriteFile(f, []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}

			got, err := readEnvFile(f)
			if tc.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("readEnvFile() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReadEnvFileNotFound(t *testing.T) {
	_, err := readEnvFile("nonexistent.env")
	if err == nil {
		t.Error("expected error for missing file, got nil")
	}
}
