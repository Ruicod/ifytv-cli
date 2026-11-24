package install

import (
	"strings"
	"testing"
)

func TestUserPathPreservesRegistryTypeAndExistingRawEntries(t *testing.T) {
	const home = `C:\Users\名字 With Space`
	const dir = home + `\.local\bin`
	t.Setenv("USERPROFILE", home)
	original := `%SystemRoot%\bin;C:\custom spacing ;`
	next, err := updatedUserPath(original, true, dir)
	if err != nil || !strings.Contains(next, original) || !strings.Contains(next, `%USERPROFILE%\.local\bin`) {
		t.Fatalf("expandable PATH corrupted: %q %v", next, err)
	}
	for _, raw := range []string{next, `"%userprofile%\.LOCAL\BIN\";other;end`} {
		got, err := updatedUserPath(raw, true, dir)
		if err != nil || got != raw {
			t.Fatalf("duplicate inserted in %q: %q %v", raw, got, err)
		}
	}
	const later = `"C:\folder;with separator";%SystemRoot%;%userprofile%\.LOCAL\BIN\;;end`
	const first = `%userprofile%\.LOCAL\BIN\;"C:\folder;with separator";%SystemRoot%;;end`
	if got, err := updatedUserPath(later, true, dir); err != nil || got != first {
		t.Fatalf("existing installation is shadowed or raw registry entries lost: %q, %v", got, err)
	}
	literal := `%USERPROFILE%\.local\bin;literal%`
	next, err = updatedUserPath(literal, false, dir)
	if err != nil || !strings.Contains(next, literal) || !strings.Contains(next, dir) {
		t.Fatalf("literal REG_SZ was expanded: %q %v", next, err)
	}
}
