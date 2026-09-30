package config

import (
	"path/filepath"
	"testing"
)

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("default config is invalid: %v", err)
	}
}

func TestValidateDataPath(t *testing.T) {
	home, err := filepath.Abs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		path string
		want bool
	}{
		{path: filepath.Join(home, "wedjat"), want: true},
		{path: "relative/path", want: false},
		{path: string(filepath.Separator), want: false},
		{path: "/home/someone", want: false},
		{path: "/root/wedjat", want: false},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			err := ValidateDataPath(test.path)
			if (err == nil) != test.want {
				t.Fatalf("ValidateDataPath(%q) error = %v, want valid=%t", test.path, err, test.want)
			}
		})
	}
}
