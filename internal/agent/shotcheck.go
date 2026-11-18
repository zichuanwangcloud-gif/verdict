package agent

import (
	"bytes"
	"crypto/sha256"
	"image/png"
	"os"
	"path/filepath"
	"strings"
)

const (
	// minShotBytes 以下的 PNG 大概率是空白页或加载失败的占位图。
	minShotBytes = 8 << 10
	// minShotSide 是截图较短边的下限，太小的图看不清内容。
	minShotSide = 200
	// sampleSide 是降采样后的边长：用来统计颜色数和计算指纹。
	sampleSide = 32
	// maxColorsAlone 以下（含）的唯一颜色数视为纯色 / 近乎空白。
	maxColorsAlone = 2
)

// inspectShot 对一张截图做基础检查，返回问题描述（没有问题时返回空串）。
//
// 检查刻意保守：它给出的结论只用于提示（拼进下一阶段的提示词、在前端打标），
// 不直接否决验收结论——深色主题的纯色页、渐变页都可能被误判，
// 最终判断交给读图的模型。
func inspectShot(path string, size int64, seen map[[16]byte]string) string {
	if !strings.EqualFold(filepath.Ext(path), ".png") {
		return "" // 只处理截图；视频、日志等不检查
	}
	if size < minShotBytes {
		return "文件过小，疑似空白页或加载失败"
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return "PNG 解码失败，文件可能损坏"
	}
	b := img.Bounds()
	if b.Dx() < minShotSide || b.Dy() < minShotSide {
		return "尺寸过小，内容不易辨认"
	}

	colors := map[uint32]struct{}{}
	var sample bytes.Buffer
	step := max(1, max(b.Dx(), b.Dy())/sampleSide)
	for y := b.Min.Y; y < b.Max.Y; y += step {
		for x := b.Min.X; x < b.Max.X; x += step {
			r, g, bl, a := img.At(x, y).RGBA()
			colors[(r>>8<<24)|(g>>8<<16)|(bl>>8<<8)|(a>>8)] = struct{}{}
			sample.WriteByte(byte(r >> 8))
			sample.WriteByte(byte(g >> 8))
			sample.WriteByte(byte(bl >> 8))
		}
	}
	if len(colors) <= maxColorsAlone {
		return "画面几乎是纯色，疑似空白页"
	}
	sum := sha256.Sum256(sample.Bytes())
	var key [16]byte
	copy(key[:], sum[:16])
	if prev, ok := seen[key]; ok {
		return "与本次产出中的 " + prev + " 完全相同，可能是重复截图"
	}
	seen[key] = filepath.Base(path)
	return ""
}
