package main

// 園のログインの部品。backend/app/security.py の移植。園のトークンは推測できない乱数で、DB にはそのハッシュ（sha256）だけを置く。
// パスワードは argon2id（argon2-cffi の既定と同じ: t=3, m=64MiB, p=4, 出力 32 バイト, ソルト 16 バイト。形式は $argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>）。
// 求職者のログインは Better Auth の JWT（jwt.go・auth.go）で、別の仕組み。

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	sessionDays     = 14
	argonTime       = 3
	argonMemoryKiB  = 65536
	argonThreads    = 4
	argonKeyLen     = 32
	argonSaltLen    = 16
	argonVersion    = 19
	detailBadLogin  = "メールアドレスまたはパスワードが違います"
	detailLoginLock = "ログインの試行が多すぎます。しばらくしてからもう一度お試しください"
)

var b64NoPad = base64.RawStdEncoding

func digest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func newToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("乱数を読めません")
	}
	return base64.RawURLEncoding.EncodeToString(b) // secrets.token_urlsafe(32) と同じ（43 文字）
}

func hashPassword(password string) string {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		panic("乱数を読めません")
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argonVersion, argonMemoryKiB, argonTime, argonThreads, b64NoPad.EncodeToString(salt), b64NoPad.EncodeToString(key))
}

type argonParams struct {
	kind                  string
	version               int
	memory, time, threads uint32
	salt, hash            []byte
}

func parseArgon(encoded string) (argonParams, bool) {
	var p argonParams
	parts := strings.Split(encoded, "$")
	// ["", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash]
	if len(parts) != 6 || parts[0] != "" || (parts[1] != "argon2id" && parts[1] != "argon2i" && parts[1] != "argon2d") {
		return p, false
	}
	p.kind = parts[1]
	if !strings.HasPrefix(parts[2], "v=") {
		return p, false
	}
	v, err := strconv.Atoi(parts[2][2:])
	if err != nil {
		return p, false
	}
	p.version = v
	for _, kv := range strings.Split(parts[3], ",") {
		k, val, ok := strings.Cut(kv, "=")
		if !ok {
			return p, false
		}
		n, err := strconv.ParseUint(val, 10, 32)
		if err != nil {
			return p, false
		}
		switch k {
		case "m":
			p.memory = uint32(n)
		case "t":
			p.time = uint32(n)
		case "p":
			p.threads = uint32(n)
		default:
			return p, false
		}
	}
	if p.memory == 0 || p.time == 0 || p.threads == 0 || p.threads > 255 || p.memory > 1<<22 { // 外から来るハッシュで、巨大なメモリを確保させない
		return p, false
	}
	if p.salt, err = b64NoPad.DecodeString(parts[4]); err != nil || len(p.salt) == 0 {
		return p, false
	}
	if p.hash, err = b64NoPad.DecodeString(parts[5]); err != nil || len(p.hash) < 4 {
		return p, false
	}
	return p, true
}

// verifyPassword は、保存されたハッシュとパスワードが合うか。ハッシュの形が読めないときも false。
func verifyPassword(stored, password string) bool {
	p, ok := parseArgon(stored)
	if !ok || p.version != argonVersion {
		return false
	}
	var key []byte
	switch p.kind {
	case "argon2id":
		key = argon2.IDKey([]byte(password), p.salt, p.time, p.memory, uint8(p.threads), uint32(len(p.hash)))
	case "argon2i":
		key = argon2.Key([]byte(password), p.salt, p.time, p.memory, uint8(p.threads), uint32(len(p.hash)))
	default:
		return false
	}
	return subtle.ConstantTimeCompare(key, p.hash) == 1
}

// needsRehash は、設定を強めたとき、次のログインで新しいハッシュに更新するための判定（argon2-cffi の check_needs_rehash と同じ）。
func needsRehash(stored string) bool {
	p, ok := parseArgon(stored)
	if !ok {
		return true
	}
	return p.kind != "argon2id" || p.version != argonVersion || p.time != argonTime || p.memory != argonMemoryKiB || p.threads != argonThreads ||
		len(p.hash) != argonKeyLen || len(p.salt) != argonSaltLen
}
