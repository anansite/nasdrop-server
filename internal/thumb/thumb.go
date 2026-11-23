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
	"sync"
)

// genLimit 控制原图解码与缩放的最大并发数，防止软路由/低配 NAS 内存溢出 (OOM)
var genLimit = make(chan struct{}, 2)

// 轻量级单飞调用管理器：对同一张图片的并发生成请求进行合并，避免重复解码消耗 CPU
type call struct {
	wg  sync.WaitGroup
	val string
	err error
}

type singleflightGroup struct {
	mu sync.Mutex
	m  map[string]*call
}

func (g *singleflightGroup) Do(key string, fn func() (string, error)) (string, error) {
	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[string]*call)
	}
	if c, ok := g.m[key]; ok {
		g.mu.Unlock()
		c.wg.Wait()
		return c.val, c.err
	}
	c := new(call)
	c.wg.Add(1)
	g.m[key] = c
	g.mu.Unlock()

	c.val, c.err = fn()
	c.wg.Done()

	g.mu.Lock()
	delete(g.m, key)
	g.mu.Unlock()

	return c.val, c.err
}

var flightGroup singleflightGroup

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
// 优先命中同级伴生缩略图 (.thumbnails/) 与磁盘持久化缓存，零重复解码；严格防御路径穿越
func GetOrGenerate(dataDir, cacheDir, relPath string, maxDim int) (string, error) {
	cleanRel := filepath.Clean(filepath.FromSlash(strings.Trim(relPath, "/")))
	if cleanRel == "." || cleanRel == "" || strings.HasPrefix(cleanRel, "..") || filepath.IsAbs(cleanRel) {
		return "", fmt.Errorf("invalid path: path traversal detected")
	}

	absData, err := filepath.Abs(dataDir)
	if err != nil {
		return "", fmt.Errorf("resolve data dir failed: %w", err)
	}
	fullPath := filepath.Join(absData, cleanRel)
	if !strings.HasPrefix(fullPath, absData) {
		return "", fmt.Errorf("access denied: path outside data directory")
	}

	// 1. 优先检查同级伴生缩略图 .thumbnails/<filename>（毫秒级秒开）
	parent := filepath.Dir(cleanRel)
	filename := filepath.Base(cleanRel)
	var companionPath string
	if parent == "." || parent == "" {
		companionPath = filepath.Join(absData, ".thumbnails", filename)
	} else {
		companionPath = filepath.Join(absData, parent, ".thumbnails", filename)
	}
	if info, err := os.Stat(companionPath); err == nil && !info.IsDir() && info.Size() > 0 {
		return companionPath, nil
	}

	// 2. 检查缓存目录中的持久化缩略图
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return "", err
	}
	h := md5.New()
	io.WriteString(h, cleanRel)
	io.WriteString(h, fmt.Sprintf(":%d", maxDim))
	hashKey := hex.EncodeToString(h.Sum(nil))
	cachedFile := filepath.Join(cacheDir, hashKey+".jpg")

	if info, err := os.Stat(cachedFile); err == nil && !info.IsDir() && info.Size() > 0 {
		return cachedFile, nil
	}

	// 3. 经由 SingleFlight 和并发信号量进行原图解码与轻量缩放
	return flightGroup.Do(hashKey, func() (string, error) {
		// 双重检查：避免排队等待时前序协程已生成完毕
		if info, err := os.Stat(cachedFile); err == nil && !info.IsDir() && info.Size() > 0 {
			return cachedFile, nil
		}

		// 获取并发生成令牌，排队防止高并发解码引发 OOM
		genLimit <- struct{}{}
		defer func() { <-genLimit }()

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
	})
}

// DeleteCache 清理某个相对路径对应的持久化缩略图缓存
func DeleteCache(cacheDir, relPath string) {
	cleanRel := filepath.Clean(filepath.FromSlash(strings.Trim(relPath, "/")))
	for _, dim := range []int{320, 640, 1024} {
		h := md5.New()
		io.WriteString(h, cleanRel)
		io.WriteString(h, fmt.Sprintf(":%d", dim))
		hashKey := hex.EncodeToString(h.Sum(nil))
		_ = os.Remove(filepath.Join(cacheDir, hashKey+".jpg"))
	}
}

// ServeThumbnail 响应缩略图 HTTP 请求，带有完备的 HTTP 客户端强缓存头
func ServeThumbnail(w http.ResponseWriter, r *http.Request, thumbFile string) {
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeFile(w, r, thumbFile)
}
