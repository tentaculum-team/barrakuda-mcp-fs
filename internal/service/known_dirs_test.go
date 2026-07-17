package service

import (
	"os"
	"testing"
)

func TestKnownDirsHomeFirstAndAllExist(t *testing.T) {
	dirs := KnownDirs()
	if len(dirs) == 0 {
		t.Fatal("KnownDirs vazio — home deveria sempre entrar")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("sem home dir neste ambiente: %v", err)
	}
	if dirs[0].Name != "home" || !pathsEqual(dirs[0].Path, home) {
		t.Fatalf("primeira entrada deve ser home %q, veio %+v", home, dirs[0])
	}
	for _, d := range dirs {
		info, err := os.Stat(d.Path)
		if err != nil || !info.IsDir() {
			t.Errorf("entrada %q (%q) não é diretório existente (err=%v)", d.Name, d.Path, err)
		}
	}
}
