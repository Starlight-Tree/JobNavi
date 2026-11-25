package main

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

// 期待値は botocore（S3SigV4Auth）が、同じ鍵・時刻・ヘッダーで作った署名（2026-11-25T18:54:11Z。Python 側で採取した）。
func TestSignV4MatchesBotocore(t *testing.T) {
	at := time.Date(2026, 11, 25, 18, 54, 11, 0, time.UTC)
	const host = "acct.r2.cloudflarestorage.com"
	hello := sha256.Sum256([]byte("hello"))
	cases := []struct {
		name, method, uri, query, payload string
		extra                             map[string]string
		want                              string
	}{
		{"get", "GET", "/bucket/images/a.jpg", "", emptyPayloadHash, nil,
			"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261125/auto/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=324ffb6072e8113840db8d451459c4d5d2ba6e2cd93ec721d7ff5ad52a25c272"},
		{"head", "HEAD", "/bucket/images/a.jpg", "", emptyPayloadHash, nil,
			"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261125/auto/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=f11c17fc513d1c0a43b08a505f060847e725fd942f2571e351dac07053f22381"},
		{"delete", "DELETE", "/bucket/images/a.jpg", "", emptyPayloadHash, nil,
			"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261125/auto/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=5cc53d0550743b303ac1ffe518ea87a5aab15d5a96815ba445339c51bc9679a1"},
		{"put", "PUT", "/bucket/images/a.jpg", "", hex.EncodeToString(hello[:]),
			map[string]string{"Content-Type": "image/jpeg", "Cache-Control": "public, max-age=31536000, immutable"},
			"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261125/auto/s3/aws4_request, SignedHeaders=cache-control;content-type;host;x-amz-content-sha256;x-amz-date, Signature=80ebdaead46b27574023bc4f3da90d8c0cb69815e5cdcdd17987e0d61c93941f"},
		{"list", "GET", "/bucket", canonicalQueryOf(map[string]string{"list-type": "2", "prefix": "images/", "continuation-token": "a+b"}), emptyPayloadHash, nil,
			"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261125/auto/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=a6d9fa1acacd2d38e116439962c6fbb1715933f4a156b528fd60a70409facacc"},
	}
	for _, c := range cases {
		got := signV4("AKIDEXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", c.method, host, c.uri, c.query, c.payload, c.extra, at)
		if got.authorization != c.want {
			t.Errorf("%s: 署名が botocore と違います\n got: %s\nwant: %s", c.name, got.authorization, c.want)
		}
		if got.amzDate != "20261125T185411Z" {
			t.Errorf("%s: 日時 %s", c.name, got.amzDate)
		}
	}
}
