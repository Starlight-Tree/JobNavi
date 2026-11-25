package main

// 画像の検証と、メタデータ（EXIF・ICC・テキスト）を除いた再エンコード。backend/app/image_service.py の process_image の移植（Pillow の代わり）。
//   - 受け付けるのは JPEG・PNG・WebP。アニメーションは不可。短辺 200px 以上・総画素 4,000 万以下。長辺は 2400px まで縮める
//   - 向き（EXIF）は画素に反映してから、メタデータを捨てる。再エンコードなので、元のバイト列は残らない
//   - 縮小は CatmullRom（Pillow は Lanczos）。JPEG は baseline（Pillow は progressive・optimize）。出力のバイト列は Pillow と同じにはならない

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math"

	"github.com/gen2brain/webp"
	xdraw "golang.org/x/image/draw"
)

const (
	imgMinSide    = 200         // 入力の短辺の下限（px）
	imgMaxPixels  = 40_000_000  // 入力の総画素の上限（展開して膨らむ画像への備え）
	imgBombPixels = 160_000_000 // これを超えると、Pillow は「読み込めません」にする（MAX_IMAGE_PIXELS の 2 倍）
	imgOutMaxEdge = 2400        // 配信用に縮める長辺の上限（px）
)

const (
	msgImageEmpty      = "画像が空です"
	msgImageAnimated   = "アニメーションの画像は登録できません"
	msgImageTooManyPx  = "画像の画素数が大きすぎます"
	msgImageUnreadable = "画像として読み込めません（形式が違うか、壊れています）"
)

// imageFormats は、形式ごとの (MIME, 拡張子)。
var imageFormats = map[string][2]string{"JPEG": {"image/jpeg", "jpg"}, "PNG": {"image/png", "png"}, "WEBP": {"image/webp", "webp"}}

// imageRejected は受け付けられない画像。メッセージは、そのまま利用者に見せてよい固定の説明。
type imageRejected struct{ msg string }

func (e *imageRejected) Error() string { return e.msg }

func reject(msg string) error { return &imageRejected{msg} }

type processed struct {
	data          []byte
	mime, ext     string
	width, height int
	sha256        string
}

func sniffFormat(raw []byte) string {
	switch {
	case len(raw) >= 3 && raw[0] == 0xFF && raw[1] == 0xD8 && raw[2] == 0xFF:
		return "JPEG"
	case bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")):
		return "PNG"
	case len(raw) >= 12 && string(raw[0:4]) == "RIFF" && string(raw[8:12]) == "WEBP":
		return "WEBP"
	}
	return ""
}

// pngAnimated は、acTL チャンクのフレーム数が 2 以上か（APNG）。
func pngAnimated(raw []byte) bool {
	for i := 8; i+8 <= len(raw); {
		size := int(binary.BigEndian.Uint32(raw[i : i+4]))
		typ := string(raw[i+4 : i+8])
		if typ == "acTL" && size >= 4 && i+12 <= len(raw) {
			return binary.BigEndian.Uint32(raw[i+8:i+12]) > 1
		}
		if typ == "IDAT" || size < 0 {
			return false
		}
		i += 12 + size
	}
	return false
}

// webpAnimated は、VP8X のアニメーションの印が立っているか。
func webpAnimated(raw []byte) bool {
	return len(raw) >= 21 && string(raw[12:16]) == "VP8X" && raw[20]&0x02 != 0
}

// ---- EXIF の向き ----

// tiffOrientation は、TIFF 形式の EXIF から、向き（1〜8）を読む。無い・不正なら 1。
func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[0:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	if bo.Uint16(t[2:4]) != 42 {
		return 1
	}
	off := int(bo.Uint32(t[4:8]))
	if off < 8 || off+2 > len(t) {
		return 1
	}
	n := int(bo.Uint16(t[off : off+2]))
	for i := 0; i < n; i++ {
		e := off + 2 + i*12
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:e+2]) == 0x0112 && bo.Uint16(t[e+2:e+4]) == 3 && bo.Uint32(t[e+4:e+8]) == 1 {
			if v := int(bo.Uint16(t[e+8 : e+10])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

func stripExifHeader(b []byte) []byte {
	return bytes.TrimPrefix(b, []byte("Exif\x00\x00"))
}

func exifOrientation(format string, raw []byte) int {
	switch format {
	case "JPEG":
		for i := 2; i+4 <= len(raw); {
			if raw[i] != 0xFF {
				return 1
			}
			marker := raw[i+1]
			if marker == 0xDA || marker == 0xD9 { // 画素のデータに入った
				return 1
			}
			if marker >= 0xD0 && marker <= 0xD8 || marker == 0x01 {
				i += 2
				continue
			}
			size := int(binary.BigEndian.Uint16(raw[i+2 : i+4]))
			if size < 2 || i+2+size > len(raw) {
				return 1
			}
			if marker == 0xE1 && bytes.HasPrefix(raw[i+4:i+2+size], []byte("Exif\x00\x00")) {
				return tiffOrientation(raw[i+4+6 : i+2+size])
			}
			i += 2 + size
		}
	case "PNG":
		for i := 8; i+8 <= len(raw); {
			size := int(binary.BigEndian.Uint32(raw[i : i+4]))
			typ := string(raw[i+4 : i+8])
			if size < 0 || i+8+size > len(raw) {
				return 1
			}
			if typ == "eXIf" {
				return tiffOrientation(stripExifHeader(raw[i+8 : i+8+size]))
			}
			if typ == "IDAT" {
				return 1
			}
			i += 12 + size
		}
	case "WEBP":
		for i := 12; i+8 <= len(raw); {
			typ := string(raw[i : i+4])
			size := int(binary.LittleEndian.Uint32(raw[i+4 : i+8]))
			if size < 0 || i+8+size > len(raw) {
				return 1
			}
			if typ == "EXIF" {
				return tiffOrientation(stripExifHeader(raw[i+8 : i+8+size]))
			}
			i += 8 + size + size%2
		}
	}
	return 1
}

// transpose は、EXIF の向きを画素に反映する（Pillow の ImageOps.exif_transpose と同じ対応）。
func transpose(src *image.NRGBA, orientation int) *image.NRGBA {
	if orientation <= 1 || orientation > 8 {
		return src
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	dw, dh := w, h
	if orientation >= 5 {
		dw, dh = h, w
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			var sx, sy int
			switch orientation {
			case 2: // FLIP_LEFT_RIGHT
				sx, sy = w-1-x, y
			case 3: // ROTATE_180
				sx, sy = w-1-x, h-1-y
			case 4: // FLIP_TOP_BOTTOM
				sx, sy = x, h-1-y
			case 5: // TRANSPOSE
				sx, sy = y, x
			case 6: // ROTATE_270（時計回りに 90 度）
				sx, sy = y, h-1-x
			case 7: // TRANSVERSE
				sx, sy = w-1-y, h-1-x
			case 8: // ROTATE_90（反時計回りに 90 度）
				sx, sy = w-1-y, x
			}
			si := src.PixOffset(sx, sy)
			di := dst.PixOffset(x, y)
			copy(dst.Pix[di:di+4], src.Pix[si:si+4])
		}
	}
	return dst
}

// thumbnailSize は、Pillow の Image.thumbnail((edge, edge)) と同じ大きさの決め方。縮める必要が無ければ ok=false。
func thumbnailSize(w, h, edge int) (nw, nh int, ok bool) {
	if edge >= w && edge >= h {
		return w, h, false
	}
	aspect := float64(w) / float64(h)
	roundAspect := func(n float64, key func(float64) float64) int {
		lo, hi := math.Floor(n), math.Ceil(n)
		best := lo
		if key(hi) < key(lo) {
			best = hi
		}
		if best < 1 {
			return 1
		}
		return int(best)
	}
	x, y := edge, edge
	if float64(x)/float64(y) >= aspect {
		x = roundAspect(float64(y)*aspect, func(n float64) float64 { return math.Abs(aspect - n/float64(y)) })
	} else {
		y = roundAspect(float64(x)/aspect, func(n float64) float64 {
			if n == 0 {
				return 0
			}
			return math.Abs(aspect - float64(x)/n)
		})
	}
	return x, y, true
}

func decodeConfig(format string, raw []byte) (image.Config, error) {
	switch format {
	case "JPEG":
		return jpeg.DecodeConfig(bytes.NewReader(raw))
	case "PNG":
		return png.DecodeConfig(bytes.NewReader(raw))
	}
	return webp.DecodeConfig(bytes.NewReader(raw))
}

func decodeImage(format string, raw []byte) (image.Image, error) {
	switch format {
	case "JPEG":
		return jpeg.Decode(bytes.NewReader(raw))
	case "PNG":
		return png.Decode(bytes.NewReader(raw))
	}
	return webp.Decode(bytes.NewReader(raw))
}

// processImage は実データを検証し、メタデータを除いて再エンコードした配信用のバイト列を返す。
func processImage(raw []byte, maxBytes int64) (processed, error) {
	if len(raw) == 0 {
		return processed{}, reject(msgImageEmpty)
	}
	if int64(len(raw)) > maxBytes {
		return processed{}, reject(fmt.Sprintf("画像は %dMB 以下にしてください", maxBytes/(1024*1024)))
	}
	format := sniffFormat(raw)
	if format == "" {
		return processed{}, reject(msgImageUnreadable)
	}
	cfg, err := decodeConfig(format, raw)
	if err != nil || cfg.Width < 1 || cfg.Height < 1 {
		return processed{}, reject(msgImageUnreadable)
	}
	pixels := int64(cfg.Width) * int64(cfg.Height)
	if pixels > imgBombPixels {
		return processed{}, reject(msgImageUnreadable)
	}
	if (format == "PNG" && pngAnimated(raw)) || (format == "WEBP" && webpAnimated(raw)) {
		return processed{}, reject(msgImageAnimated)
	}
	if pixels > imgMaxPixels {
		return processed{}, reject(msgImageTooManyPx)
	}
	if min(cfg.Width, cfg.Height) < imgMinSide {
		return processed{}, reject(fmt.Sprintf("画像は短辺 %dpx 以上にしてください", imgMinSide))
	}
	src, err := decodeImage(format, raw)
	if err != nil {
		return processed{}, reject(msgImageUnreadable)
	}
	_, isGray := src.(*image.Gray)

	// 縮小（必要なときだけ）。先に縮めてから、向きを直す（箱が正方形なので、順序は結果に影響しない）
	b := src.Bounds()
	var work *image.NRGBA
	if nw, nh, ok := thumbnailSize(b.Dx(), b.Dy(), imgOutMaxEdge); ok {
		work = image.NewNRGBA(image.Rect(0, 0, nw, nh))
		xdraw.CatmullRom.Scale(work, work.Bounds(), src, b, xdraw.Src, nil)
	} else {
		work = image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(work, work.Bounds(), src, b.Min, draw.Src)
	}
	work = transpose(work, exifOrientation(format, raw))

	var out bytes.Buffer
	switch format {
	case "JPEG":
		var enc image.Image = work
		if isGray {
			g := image.NewGray(work.Bounds())
			draw.Draw(g, g.Bounds(), work, image.Point{}, draw.Src)
			enc = g
		}
		if err := jpeg.Encode(&out, enc, &jpeg.Options{Quality: 88}); err != nil {
			return processed{}, reject(msgImageUnreadable)
		}
	case "PNG":
		if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&out, work); err != nil {
			return processed{}, reject(msgImageUnreadable)
		}
	default:
		if err := webp.Encode(&out, work, webp.Options{Quality: 88, Method: 4}); err != nil {
			return processed{}, reject(msgImageUnreadable)
		}
	}
	data := out.Bytes()
	sum := sha256.Sum256(data)
	mime := imageFormats[format]
	return processed{data: data, mime: mime[0], ext: mime[1], width: work.Bounds().Dx(), height: work.Bounds().Dy(), sha256: hex.EncodeToString(sum[:])}, nil
}
