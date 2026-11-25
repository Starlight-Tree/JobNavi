package main

// DB を使わない単体テスト。期待値の多くは、Python（json・Pillow・argon2-cffi・pydantic）が実際に返した値（testdata/unit_fixtures.json）。

import (
	"bytes"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/gen2brain/webp"
)

type unitFixtures struct {
	JSONErrors [][]any     `json:"json_errors"`
	Thumbnail  [][]any     `json:"thumbnail"`
	Vectors    [][]any     `json:"vectors"`
	Cosine     [][]float64 `json:"cosine"`
	Sums       [][]any     `json:"sums"`
	Rounds     [][]float64 `json:"rounds"`
	Argon      map[string]string
}

func loadFixtures(t *testing.T) unitFixtures {
	t.Helper()
	raw, err := os.ReadFile("testdata/unit_fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var f unitFixtures
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestPyJSONErrorsMatchPython(t *testing.T) {
	for _, c := range loadFixtures(t).JSONErrors {
		body := c[0].(string)
		msg, pos, bad := pyJSONError([]rune(body))
		if c[1] == nil {
			if bad {
				t.Errorf("%q: 構文エラーにしてはいけません（%s @%d）", body, msg, pos)
			}
			continue
		}
		if !bad || msg != c[1].(string) || pos != int(c[2].(float64)) {
			t.Errorf("%q: got (%q, %d, %v) want (%q, %v)", body, msg, pos, bad, c[1], c[2])
		}
	}
}

func TestThumbnailSizeMatchesPillow(t *testing.T) {
	for _, c := range loadFixtures(t).Thumbnail {
		w, h := int(c[0].(float64)), int(c[1].(float64))
		want := c[2].([]any)
		nw, nh, _ := thumbnailSize(w, h, imgOutMaxEdge)
		if nw != int(want[0].(float64)) || nh != int(want[1].(float64)) {
			t.Errorf("%dx%d: got %dx%d want %v", w, h, nw, nh, want)
		}
	}
}

func TestEmbeddingMathMatchesPython(t *testing.T) {
	f := loadFixtures(t)
	vecs := make([]embedding, len(f.Vectors))
	for i, v := range f.Vectors {
		vecs[i].model = v[0].(string)
		for _, x := range v[1].([]any) {
			vecs[i].vector = append(vecs[i].vector, x.(float64))
		}
	}
	for _, c := range f.Cosine {
		if got := cosine(vecs[int(c[0])], vecs[int(c[1])]); got != c[2] {
			t.Errorf("cosine(%v, %v) = %v want %v", c[0], c[1], got, c[2])
		}
	}
	for _, c := range f.Sums {
		var xs []float64
		for _, x := range c[0].([]any) {
			xs = append(xs, x.(float64))
		}
		if got, want := pySum(xs), c[1].(float64); got != want && !(math.IsNaN(got) && math.IsNaN(want)) {
			t.Errorf("pySum(%v) = %v want %v", xs, got, want)
		}
	}
	for _, c := range f.Rounds {
		if got := pyRound9(c[0]); got != c[1] {
			t.Errorf("pyRound9(%v) = %v want %v", c[0], got, c[1])
		}
	}
}

func TestArgon2(t *testing.T) {
	f := loadFixtures(t)
	if !verifyPassword(f.Argon["id"], "secret-pw") {
		t.Error("argon2-cffi が作ったハッシュを検証できません")
	}
	if verifyPassword(f.Argon["id"], "wrong") || verifyPassword("not a hash", "x") || verifyPassword("", "x") {
		t.Error("誤ったパスワード・読めないハッシュを通しました")
	}
	if needsRehash(f.Argon["id"]) {
		t.Error("既定のパラメーターのハッシュを、作り直し対象にしました")
	}
	if !needsRehash(f.Argon["rehash"]) || !verifyPassword(f.Argon["rehash"], "secret-pw") {
		t.Error("パラメーターの違うハッシュは、検証できて、作り直し対象のはず")
	}
	own := hashPassword("日本語のパスワード")
	if !strings.HasPrefix(own, "$argon2id$v=19$m=65536,t=3,p=4$") || !verifyPassword(own, "日本語のパスワード") || needsRehash(own) {
		t.Errorf("自分で作ったハッシュ: %s", own)
	}
}

func TestParsePyInt(t *testing.T) {
	ok := map[string]int64{"1": 1, "+1": 1, " 2 ": 2, "1_0": 10, "1.0": 1, "-5": -5, "007": 7}
	for in, want := range ok {
		if n, good := parsePyInt(in); !good || n.Int64() != want {
			t.Errorf("%q: %v %v", in, n, good)
		}
	}
	for _, in := range []string{"", "abc", "1.5", "1e2", "٣", "１", "1__0", "_1", "1_", "0x10", "--1", "1.", ".5"} {
		if _, good := parsePyInt(in); good {
			t.Errorf("%q を整数として読みました", in)
		}
	}
}

func TestEmailValidation(t *testing.T) {
	good := map[string]string{
		"a@b.com": "a@b.com", " A@B.COM ": "A@b.com", "あ@b.com": "あ@b.com", "a+b@c.com": "a+b@c.com", "A@Ｂ.com": "A@b.com", "Ann <a@b.com>": "a@b.com",
		"a@b.c": "a@b.c", "a%b@c.com": "a%b@c.com", "a@b.com ": "a@b.com",
	}
	for in, want := range good {
		if got, reason := validateEmail(in); reason != "" || got != want {
			t.Errorf("%q: got (%q, %q) want %q", in, got, reason, want)
		}
	}
	bad := map[string]string{
		"abc": "An email address must have an @-sign.", "": "An email address must have an @-sign.", "a@": "There must be something after the @-sign.",
		"@b.com": "There must be something before the @-sign.", "a@b": "The part after the @-sign is not valid. It should have a period.",
		"a b@c.com": "The email address contains invalid characters before the @-sign: SPACE.", "a@b..com": "An email address cannot have two periods in a row.",
		"a@-b.com": "An email address cannot have a hyphen immediately after the @-sign.", "a@b-.com": "An email address cannot have a period and a hyphen next to each other.",
		"a@localhost": "The part after the @-sign is not valid. It should have a period.", "a@@b.com": "The part after the @-sign contains invalid characters: '@'.",
		".a@b.com": "An email address cannot start with a period.", "a.@b.com": "An email address cannot have a period immediately before the @-sign.",
		"a..b@c.com": "An email address cannot have two periods in a row.", "a@.com": "An email address cannot have a period immediately after the @-sign.",
		"a@b.com.": "An email address cannot end with a period.", "a@b_c.com": "The part after the @-sign contains invalid characters: '_'.",
		"a@[1.2.3.4]": "A bracketed IP address after the @-sign is not allowed here.", "\"a b\"@c.com": "Quoting the part before the @-sign is not allowed here.",
		"a@b.test": "The part after the @-sign is a special-use or reserved name that cannot be used with email.", "a@example.invalid": "The part after the @-sign is a special-use or reserved name that cannot be used with email.",
		"a\n@b.com": "Carriage return and line feed characters are not allowed", "a@1.2.3.4": "The part after the @-sign is not valid. It is not within a valid top-level domain.",
		"a@b.123": "The part after the @-sign is not valid. It is not within a valid top-level domain.", "a@b,com": "The part after the @-sign contains invalid characters: ','.",
		"mailto:a@b.com": "The email address contains invalid characters before the @-sign: ':'.", "a@b.com a": "The part after the @-sign contains invalid characters: SPACE.",
		"a@" + strings.Repeat("b", 64) + ".com": "After the @-sign, periods cannot be separated by so many characters (1 character too many).",
		"a@b.com" + strings.Repeat("x", 260):    "The email address is too long after the @-sign (12 characters too many).",
	}
	for in, want := range bad {
		if _, reason := validateEmail(in); reason != want {
			t.Errorf("%q: reason %q want %q", truncate(in, 40), reason, want)
		}
	}
}

func TestUUIDParsing(t *testing.T) {
	canonical := "abcdef00-0000-0000-0000-000000000000"
	for _, in := range []string{canonical, strings.ToUpper(canonical), "{" + canonical + "}", "urn:uuid:" + canonical, strings.ReplaceAll(canonical, "-", "")} {
		if got, msg := parseUUIDStrict(in); msg != "" || got != canonical {
			t.Errorf("%q: %q %q", in, got, msg)
		}
	}
	bad := map[string]string{
		"x": "invalid character: found `x` at 0", "": "invalid length: found 0", "1234": "invalid length: found 4", strings.Repeat("0", 31): "invalid length: found 31",
		"00000000-0000-0000-0000-00000000000":  "invalid group length in group 4: expected 12, found 11",
		"0000000-00000-0000-0000-000000000000": "invalid group length in group 0: expected 8, found 7",
		"g0000000-0000-0000-0000-000000000000": "invalid character: found `g` at 0", "00000000_0000_0000_0000_000000000000": "invalid character: found `_` at 8",
		" 00000000-0000-0000-0000-000000000000": "invalid character: found ` ` at 0", "0000-0000-0000-0000-0000-0000-0000-0000": "invalid group count: expected 5, found 8",
		"{00000000000000000000000000000000}": "invalid group count: expected 5, found 1", strings.Repeat("-", 36): "invalid group count: expected 5, found 37",
		"{00000000-0000-0000-0000-000000000000": "invalid character: found `{` at 0", "00000000-0000-0000-0000-000000000000}": "invalid character: found `}` at 36",
		strings.Repeat("ａ", 32): "invalid length: found 96",
	}
	for in, want := range bad {
		if _, msg := parseUUIDStrict(in); msg != want {
			t.Errorf("%q: %q want %q", truncate(in, 40), msg, want)
		}
	}
	if id, ok := parseUUIDLoose("{" + strings.ToUpper(canonical) + "}"); !ok || id != canonical {
		t.Error("parseUUIDLoose")
	}
	if _, ok := parseUUIDLoose("zz"); ok {
		t.Error("parseUUIDLoose が不正な値を通しました")
	}
}

func TestKeywordTermsAndUsable(t *testing.T) {
	got := keywordTerms("  温室　ガーデン 温室\t a b c d e ")
	want := []string{"温室", "ガーデン", "a", "b", "c"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("keywordTerms = %v", got)
	}
	if len(keywordTerms("")) != 0 || len(keywordTerms(" 　 ")) != 0 {
		t.Error("空白だけのキーワードは語が無いはず")
	}
	for _, in := range [][]string{{"温室"}, {"a_b"}, {"ガーデン", "x"}} {
		if !usableTerms(in) {
			t.Errorf("%v は渡してよいはず", in)
		}
	}
	for _, in := range [][]string{{""}, {"-x"}, {`"x"`}, {"！"}, {"・"}, {"ok", "-"}} {
		if usableTerms(in) {
			t.Errorf("%v は渡してはいけない", in)
		}
	}
}

func TestCheckKey(t *testing.T) {
	for _, k := range []string{"images/a.jpg", "a", "images/0123-4567.webp"} {
		if _, err := checkKey(k); err != nil {
			t.Errorf("%q: %v", k, err)
		}
	}
	for _, k := range []string{"", "/a", "../a", "a/../b", "a//b", "a/", "A.jpg", "a b", "a\n", strings.Repeat("a", 202)} {
		if _, err := checkKey(k); err == nil {
			t.Errorf("%q を通しました", k)
		}
	}
}

func TestConfigErrors(t *testing.T) {
	base := map[string]string{"DATABASE_URL": "postgresql://x/y"}
	with := func(kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	if _, err := loadConfig(testEnvMap(map[string]string{})); err == nil {
		t.Error("DATABASE_URL が無いのに通りました")
	}
	bad := []map[string]string{
		with("STORAGE_BACKEND", "gcs"), with("SEARCH_ENGINE", "solr"), with("SEARCH_ENGINE", "meilisearch"), with("SEARCH_ENGINE", "meilisearch", "MEILI_URL", "ftp://x"),
		with("SEARCH_ENGINE", "meilisearch", "MEILI_URL", "http://m", "MEILI_API_KEY", "日本語"), with("SEARCH_ENGINE", "meilisearch", "MEILI_URL", "http://m", "MEILI_TIMEOUT_SECONDS", "0"),
		with("GARDEN_LOGIN_MAX_FAILURES", "0"), with("GARDEN_LOGIN_MAX_FAILURES", "x"), with("GARDEN_LOGIN_WINDOW_MINUTES", "10081"), with("AUTH_BASE_URL", "ftp://x"), with("AUTH_JWKS_URL", "no-scheme"),
		with("STORAGE_BACKEND", "s3"), with("STORAGE_BACKEND", "s3", "STORAGE_S3_ENDPOINT_URL", "https://e", "STORAGE_S3_BUCKET", "b", "STORAGE_S3_ACCESS_KEY_ID", "k"),
		with("IMAGE_PUBLIC_BASE_URL", ""), with("IMAGE_MAX_UPLOAD_BYTES", "-1"),
	}
	for i, m := range bad {
		if _, err := loadConfig(testEnvMap(m)); err == nil {
			t.Errorf("設定ミスが通りました: #%d %v", i, m)
		}
	}
	cfg, err := loadConfig(testEnvMap(with("CORS_ORIGINS", " http://a , ,http://b ")))
	if err != nil || strings.Join(cfg.CORSOrigins, "|") != "http://a|http://b" || cfg.PublicBaseURL != "/media" || cfg.GardenMaxFailures != 5 {
		t.Errorf("既定値: %+v %v", cfg, err)
	}
	if cfg, _ := loadConfig(testEnvMap(with("STORAGE_BACKEND", "s3", "STORAGE_S3_ENDPOINT_URL", "https://e", "STORAGE_S3_BUCKET", "b", "STORAGE_S3_ACCESS_KEY_ID", "k", "STORAGE_S3_SECRET_ACCESS_KEY", "s", "IMAGE_PUBLIC_BASE_URL", "https://cdn.example/"))); cfg.PublicBaseURL != "https://cdn.example" {
		t.Errorf("公開 URL の末尾の / を除いていません: %q", cfg.PublicBaseURL)
	}
}

// ---- 画像 ----

func gradient(w, h int) *image.NRGBA {
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			im.SetNRGBA(x, y, color.NRGBA{uint8(x * 255 / w), uint8(y * 255 / h), 80, 255})
		}
	}
	return im
}

func encodeJPEG(t *testing.T, im image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, im, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func encodePNG(t *testing.T, im image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func encodeWebP(t *testing.T, im image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := webp.Encode(&b, im, webp.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// WebP の復号は 0〜255 の範囲を保つ。webp.Decode の YUV（16〜235）を Go の標準の変換で読むと、黒が 16・白が 235 に縮む。
func TestDecodeWebPKeepsRangeAndAlpha(t *testing.T) {
	abs := func(x int) int {
		if x < 0 {
			return -x
		}
		return x
	}
	ramp := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			v := uint8(x * 255 / 63)
			ramp.SetNRGBA(x, y, color.NRGBA{v, v, v, 255})
		}
	}
	for _, o := range []webp.Options{{Quality: 100}, {Lossless: true}} {
		var b bytes.Buffer
		if err := webp.Encode(&b, ramp, o); err != nil {
			t.Fatal(err)
		}
		got, err := decodeImage("WEBP", b.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		black := color.NRGBAModel.Convert(got.At(0, 32)).(color.NRGBA)
		white := color.NRGBAModel.Convert(got.At(63, 32)).(color.NRGBA)
		if black.R > 4 || white.R < 251 {
			t.Errorf("lossless=%v: 黒 %d・白 %d（0 付近・255 付近のはず）", o.Lossless, black.R, white.R)
		}
	}

	// 半透明の色が保たれる（乗算済みの RGBA で返るので、NRGBA に直して比べる。8 ビットの丸めの分だけ許す）
	half := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	for i := 0; i < len(half.Pix); i += 4 {
		half.Pix[i], half.Pix[i+1], half.Pix[i+2], half.Pix[i+3] = 200, 100, 50, 128
	}
	var b bytes.Buffer
	if err := webp.Encode(&b, half, webp.Options{Lossless: true}); err != nil {
		t.Fatal(err)
	}
	got, err := decodeImage("WEBP", b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	c := color.NRGBAModel.Convert(got.At(16, 16)).(color.NRGBA)
	if abs(int(c.R)-200) > 2 || abs(int(c.G)-100) > 2 || abs(int(c.B)-50) > 2 || abs(int(c.A)-128) > 1 {
		t.Errorf("半透明の色が変わった: %v（200,100,50,128 のはず）", c)
	}
}

func TestProcessImageAcceptsAndNormalizes(t *testing.T) {
	cases := []struct {
		name, mime, ext string
		data            []byte
		w, h            int
	}{
		{"jpeg", "image/jpeg", "jpg", encodeJPEG(t, gradient(800, 600)), 800, 600},
		{"png", "image/png", "png", encodePNG(t, gradient(500, 300)), 500, 300},
		{"webp", "image/webp", "webp", encodeWebP(t, gradient(400, 400)), 400, 400},
		{"wide jpeg is shrunk", "image/jpeg", "jpg", encodeJPEG(t, gradient(3000, 400)), 2400, 320},
		{"tall png is shrunk", "image/png", "png", encodePNG(t, gradient(300, 3000)), 240, 2400},
	}
	for _, c := range cases {
		p, err := processImage(c.data, 5<<20)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if p.mime != c.mime || p.ext != c.ext || p.width != c.w || p.height != c.h || len(p.sha256) != 64 || len(p.data) == 0 {
			t.Errorf("%s: %+v（期待 %s %s %dx%d）", c.name, struct {
				mime, ext string
				w, h      int
			}{p.mime, p.ext, p.width, p.height}, c.mime, c.ext, c.w, c.h)
		}
		if cfg, format, err := image.DecodeConfig(bytes.NewReader(p.data)); c.ext != "webp" && (err != nil || cfg.Width != c.w || cfg.Height != c.h || format == "") {
			t.Errorf("%s: 出力を読めません: %v", c.name, err)
		}
	}
}

func TestProcessImageRejects(t *testing.T) {
	animated := encodePNG(t, gradient(300, 300))
	// APNG の印（acTL: フレーム数 2）を IHDR の直後に足す
	acTL := append([]byte{0, 0, 0, 8}, []byte("acTL")...)
	acTL = append(acTL, 0, 0, 0, 2, 0, 0, 0, 0)
	acTL = append(acTL, crcOf("acTL", []byte{0, 0, 0, 2, 0, 0, 0, 0})...)
	animated = append(append(append([]byte{}, animated[:33]...), acTL...), animated[33:]...)
	huge := encodePNG(t, image.NewGray(image.Rect(0, 0, 7000, 7000)))
	cases := []struct {
		name string
		data []byte
		max  int64
		msg  string
	}{
		{"empty", nil, 5 << 20, "画像が空です"},
		{"too big", make([]byte, 6<<20), 5 << 20, "画像は 5MB 以下にしてください"},
		{"not an image", bytes.Repeat([]byte("abc"), 100), 5 << 20, msgImageUnreadable},
		{"truncated jpeg", encodeJPEG(t, gradient(800, 600))[:2000], 5 << 20, msgImageUnreadable},
		{"gif", []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;"), 5 << 20, msgImageUnreadable},
		{"tiny", encodePNG(t, gradient(100, 100)), 5 << 20, "画像は短辺 200px 以上にしてください"},
		{"thin", encodePNG(t, gradient(199, 900)), 5 << 20, "画像は短辺 200px 以上にしてください"},
		{"apng", animated, 5 << 20, msgImageAnimated},
		{"too many pixels", huge, 20 << 20, msgImageTooManyPx},
	}
	for _, c := range cases {
		_, err := processImage(c.data, c.max)
		var rej *imageRejected
		if err == nil || !asRejected(err, &rej) || rej.msg != c.msg {
			t.Errorf("%s: %v（期待 %q）", c.name, err, c.msg)
		}
	}
}

func asRejected(err error, target **imageRejected) bool {
	r, ok := err.(*imageRejected)
	if ok {
		*target = r
	}
	return ok
}

// 向き（EXIF）を画素に反映する。PNG の eXIf チャンクで、左上が赤・右上が緑・左下が青の画像を回す。
func TestEXIFOrientationIsApplied(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 300, 200))
	red, green, blue, white := color.NRGBA{255, 0, 0, 255}, color.NRGBA{0, 255, 0, 255}, color.NRGBA{0, 0, 255, 255}, color.NRGBA{255, 255, 255, 255}
	for y := 0; y < 200; y++ {
		for x := 0; x < 300; x++ {
			switch {
			case x < 150 && y < 100:
				src.SetNRGBA(x, y, red)
			case x >= 150 && y < 100:
				src.SetNRGBA(x, y, green)
			case x < 150:
				src.SetNRGBA(x, y, blue)
			default:
				src.SetNRGBA(x, y, white)
			}
		}
	}
	type corner struct{ tl, tr, bl color.NRGBA }
	// orientation → (出力の大きさ, 左上・右上・左下の色)。Pillow の exif_transpose と同じ対応
	want := map[int]struct {
		w, h int
		c    corner
	}{
		1: {300, 200, corner{red, green, blue}},
		2: {300, 200, corner{green, red, white}},
		3: {300, 200, corner{white, blue, green}},
		4: {300, 200, corner{blue, white, red}},
		5: {200, 300, corner{red, blue, green}},
		6: {200, 300, corner{blue, red, white}},
		7: {200, 300, corner{white, green, blue}},
		8: {200, 300, corner{green, white, red}},
	}
	for o := 1; o <= 8; o++ {
		data := withPNGExifOrientation(encodePNG(t, src), o)
		p, err := processImage(data, 5<<20)
		if err != nil {
			t.Fatalf("向き %d: %v", o, err)
		}
		out, err := png.Decode(bytes.NewReader(p.data))
		if err != nil {
			t.Fatal(err)
		}
		w := want[o]
		if p.width != w.w || p.height != w.h {
			t.Errorf("向き %d: 大きさ %dx%d（期待 %dx%d）", o, p.width, p.height, w.w, w.h)
			continue
		}
		at := func(x, y int) color.NRGBA { return color.NRGBAModel.Convert(out.At(x, y)).(color.NRGBA) }
		got := corner{at(5, 5), at(p.width-5, 5), at(5, p.height-5)}
		if got != w.c {
			t.Errorf("向き %d: 角の色 %+v（期待 %+v）", o, got, w.c)
		}
	}
}

func crcOf(typ string, data []byte) []byte {
	sum := crc32IEEE(append([]byte(typ), data...))
	return []byte{byte(sum >> 24), byte(sum >> 16), byte(sum >> 8), byte(sum)}
}

// withPNGExifOrientation は、IHDR の直後に、向きだけを持つ eXIf チャンクを足す。
func withPNGExifOrientation(pngData []byte, orientation int) []byte {
	tiff := []byte{'M', 'M', 0, 42, 0, 0, 0, 8, 0, 1, 0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, byte(orientation), 0, 0, 0, 0, 0, 0}
	chunk := append([]byte{0, 0, 0, byte(len(tiff))}, []byte("eXIf")...)
	chunk = append(chunk, tiff...)
	chunk = append(chunk, crcOf("eXIf", tiff)...)
	return append(append(append([]byte{}, pngData[:33]...), chunk...), pngData[33:]...)
}

func crc32IEEE(b []byte) uint32 { return crc32.ChecksumIEEE(b) }
