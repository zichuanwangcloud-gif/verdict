package agent

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// writePNG 生成一张测试图并返回路径。
func writePNG(t *testing.T, dir, name string, w, h int, fill func(x, y int) color.Color) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, fill(x, y))
		}
	}
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestInspectShotDetectsBlank(t *testing.T) {
	dir := t.TempDir()
	// 纯白大图：尺寸足够、体积也够，只能靠颜色数判出来。
	p := writePNG(t, dir, "blank.png", 800, 600, func(int, int) color.Color { return color.White })
	info, _ := os.Stat(p)
	seen := map[[16]byte]string{}
	if got := inspectShot(p, info.Size(), seen); got == "" {
		t.Fatal("纯色图应被判为疑似空白")
	}
}

func TestInspectShotAcceptsRealPage(t *testing.T) {
	dir := t.TempDir()
	// 带纹理的图：颜色数远超阈值。
	p := writePNG(t, dir, "page.png", 800, 600, func(x, y int) color.Color {
		return color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: uint8((x * y) % 256), A: 255}
	})
	info, _ := os.Stat(p)
	if got := inspectShot(p, info.Size(), map[[16]byte]string{}); got != "" {
		t.Fatalf("正常页面不应报问题: %s", got)
	}
}

func TestInspectShotDetectsDuplicate(t *testing.T) {
	dir := t.TempDir()
	fill := func(x, y int) color.Color {
		return color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: uint8((x * y) % 256), A: 255}
	}
	a := writePNG(t, dir, "a.png", 400, 300, fill)
	b := writePNG(t, dir, "b.png", 400, 300, fill)
	info, _ := os.Stat(a)
	seen := map[[16]byte]string{}
	if got := inspectShot(a, info.Size(), seen); got != "" {
		t.Fatalf("第一张不应报问题: %s", got)
	}
	info, _ = os.Stat(b)
	if got := inspectShot(b, info.Size(), seen); got == "" {
		t.Fatal("完全相同的第二张应被判为重复")
	}
}

func TestInspectShotSmallFile(t *testing.T) {
	dir := t.TempDir()
	p := writePNG(t, dir, "tiny.png", 300, 300, func(int, int) color.Color { return color.Black })
	if got := inspectShot(p, 100, map[[16]byte]string{}); got == "" {
		t.Fatal("过小的文件应报问题")
	}
}

func TestInspectShotSkipsNonPNG(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "log.txt")
	os.WriteFile(p, []byte("hello"), 0o644)
	if got := inspectShot(p, 5, map[[16]byte]string{}); got != "" {
		t.Fatalf("非图片不应检查: %s", got)
	}
}
