package core_test

import (
	"strings"
	"testing"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core"
)

func TestReadBootIDIsTrimmedAndNonEmpty(t *testing.T) {
	id, err := core.ReadBootID()
	if err != nil {
		t.Fatalf("read boot id: %v", err)
	}
	if id == "" || id != strings.TrimSpace(id) {
		t.Fatalf("boot id = %q, want non-empty with no surrounding whitespace", id)
	}
}
