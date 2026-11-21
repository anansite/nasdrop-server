package thumb

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Resize 纯 Go 高性能等比下采样缩放
func Resize(src image.Image, maxW, maxH int) image.Image {
	bounds := src.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()
	if srcW <= 0 || srcH <= 0 {
		return src
	}

	ratioW := float64(maxW) / float64(srcW)
	ratioH := float64(maxH) / float64(srcH)
	ratio := math.Min(ratioW, ratioH)
	if ratio >= 1.0 {
		return src
	}

	dstW := int(math.Round(float64(srcW) * ratio))
	dstH := int(math.Round(float64(srcH) * ratio))
	if dstW < 1 {
		dstW = 1
	}
	if dstH < 1 {
		dstH = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	xRatio := float64(srcW) / float64(dstW)
	yRatio := float64(srcH) / float64(dstH)

	for y := 0; y < dstH; y++ {
		srcY := bounds.Min.Y + int(float64(y)*yRatio)
		for x := 0; x < dstW; x++ {
			srcX := bounds.Min.X + int(float64(x)*xRatio)
			dst.Set(x, y, src.At(srcX, srcY))
		}
	}
	return dst
}

// GetOrGenerate 获取或生成缩略图绝对路径
// 优先命中同级伴生缩略图 (.thumbnails/) 与磁盘持久化缓存，零重复解码
func GetOrGenerate(dataDir, cacheDir, relPath string, maxDim int) (string, error) {
	relPath = strings.Trim(relPath, "/")
	if relPath == "" {
		return "", fmt.Errorf("empty path")
	}

	// 1. 优先检查同级伴生缩略图 .thumbnails/<filename>
	parent := filepath.Dir(relPath)
	filename := filepath.Base(relPath)
	var companionPath string
	if parent == "." || parent == "" {
		companionPath = filepath.Join(dataDir, ".thumbnails", filename)
	} else {
		companionPath = filepath.Join(dataDir, parent, ".thumbnails", filename)
	}
	if info, err := os.Stat(companionPath); err == nil && !info.IsDir() && info.Size() > 0 {
		return companionPath, nil
	}

	// 2. 检查缓存目录中的持久化缩略图
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return "", err
	}
	h := md5.New()
	io.WriteString(h, relPath)
	io.WriteString(h, fmt.Sprintf(":%d", maxDim))
	hashKey := hex.EncodeToString(h.Sum(nil))
	cachedFile := filepath.Join(cacheDir, hashKey+".jpg")

	if info, err := os.Stat(cachedFile); err == nil && !info.IsDir() && info.Size() > 0 {
		return cachedFile, nil
	}

	// 3. 原图解码与轻量缩放
	fullPath := filepath.Join(dataDir, relPath)
	f, err := os.Open(fullPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return "", fmt.Errorf("decode image failed: %w", err)
	}

	thumb := Resize(img, maxDim, maxDim)

	tmpFile := cachedFile + ".tmp"
	out, err := os.Create(tmpFile)
	if err != nil {
		return "", err
	}
	if err := jpeg.Encode(out, thumb, &jpeg.Options{Quality: 75}); err != nil {
		out.Close()
		os.Remove(tmpFile)
		return "", err
	}
	out.Close()

	if err := os.Rename(tmpFile, cachedFile); err != nil {
		return tmpFile, nil
	}
	return cachedFile, nil
}

// ServeThumbnail 响应缩略图 HTTP 请求，带有完备的 HTTP 客户端强缓存头
func ServeThumbnail(w http.ResponseWriter, r *http.Request, thumbFile string) {
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeFile(w, r, thumbFile)
}
