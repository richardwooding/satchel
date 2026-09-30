package icon

import (
	"bytes"
	"image/png"
	"testing"
)

func TestIconsDecode(t *testing.T) {
	for name, b := range map[string][]byte{"idle": Tray(false), "active": Tray(true), "app": App(256)} {
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if img.Bounds().Dx() == 0 {
			t.Fatalf("%s is empty", name)
		}
	}
	if bytes.Equal(Tray(false), Tray(true)) {
		t.Fatal("active tray icon is indistinguishable from idle")
	}
}
