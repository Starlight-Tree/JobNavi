package main

// AWS Signature Version 4（S3 互換。R2 など）。標準ライブラリだけで書いた、最小の署名。
// 署名するヘッダーは host・x-amz-content-sha256・x-amz-date と、渡された追加のヘッダー（Content-Type・Cache-Control）。
// 期待値は botocore（S3SigV4Auth）に、固定の時刻・鍵を渡して作ったものと照合する（s3sign_test.go）。

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

const emptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" // sha256("")

// uriEncode は SigV4 の URI エンコード（RFC 3986 の予約されない文字以外を %XX にする）。encodeSlash が false なら / は残す。
func uriEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func canonicalQueryOf(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, uriEncode(k, true)+"="+uriEncode(params[k], true))
	}
	return strings.Join(parts, "&")
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

type signature struct {
	amzDate       string
	authorization string
}

func signV4(keyID, secret, method, host, canonicalURI, canonicalQuery, payloadHash string, extra map[string]string, at time.Time) signature {
	at = at.UTC()
	amzDate := at.Format("20060102T150405Z")
	day := at.Format("20060102")
	headers := map[string]string{"host": host, "x-amz-content-sha256": payloadHash, "x-amz-date": amzDate}
	for k, v := range extra {
		headers[strings.ToLower(k)] = strings.Join(strings.Fields(v), " ")
	}
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var canonicalHeaders strings.Builder
	for _, k := range names {
		canonicalHeaders.WriteString(k + ":" + headers[k] + "\n")
	}
	signedHeaders := strings.Join(names, ";")
	canonicalRequest := strings.Join([]string{method, canonicalURI, canonicalQuery, canonicalHeaders.String(), signedHeaders, payloadHash}, "\n")
	scope := day + "/" + s3Region + "/" + s3Service + "/aws4_request"
	hash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(hash[:])
	key := hmacSHA256([]byte("AWS4"+secret), day)
	key = hmacSHA256(key, s3Region)
	key = hmacSHA256(key, s3Service)
	key = hmacSHA256(key, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(key, stringToSign))
	return signature{
		amzDate:       amzDate,
		authorization: "AWS4-HMAC-SHA256 Credential=" + keyID + "/" + scope + ", SignedHeaders=" + signedHeaders + ", Signature=" + sig,
	}
}
