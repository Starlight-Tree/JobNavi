package main

// メールアドレスの検査（pydantic の EmailStr = email-validator 2.x の構文検査、到達確認なし）。園のログインの本文用。
// 誤りの文は email-validator と同じ（"value is not a valid email address: …"）。正しいものは、ドメインを NFKC・小文字にして返す（前後の空白は除く）。
// 実装する範囲: @ の有無・前後が空・空白や記号・ピリオドの並び・ハイフンの位置・ラベルと全体の長さ・括弧・引用符・特別な用途の名前・TLD が数字だけ・
// "名前 <アドレス>" の形。IDNA の変換（xn-- の復元）はしない。

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const emailPrefix = "value is not a valid email address: "

var prettyEmail = regexp.MustCompile(`^([\w ]*?) *<(.*)> *$`)

var specialUseDomains = []string{"arpa", "invalid", "local", "localhost", "onion", "test"}

func describeChar(r rune) string {
	if r == ' ' {
		return "SPACE"
	}
	if unicode.IsControl(r) || !unicode.IsPrint(r) {
		return fmt.Sprintf("U+%04X", r)
	}
	return "'" + string(r) + "'"
}

func joinUnique(chars []string) string {
	seen := map[string]bool{}
	var out []string
	for _, c := range chars {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return strings.Join(out, ", ")
}

func isAtext(r rune) bool {
	if r < 0x80 {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("!#$%&'*+/=?^_`{|}~-", r)
	}
	return unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsNumber(r)
}

func tooMany(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("(%d %s too many)", n, one)
	}
	return fmt.Sprintf("(%d %s too many)", n, many)
}

// validateEmail は、(正規化したアドレス, 誤りの理由)。理由が空でなければ誤り。
func validateEmail(in string) (string, string) {
	s := strings.TrimSpace(in)
	if m := prettyEmail.FindStringSubmatch(s); m != nil {
		s = m[2]
	}
	if strings.ContainsAny(s, "\r\n") {
		return "", "Carriage return and line feed characters are not allowed"
	}
	at := strings.Index(s, "@")
	if at < 0 {
		return "", "An email address must have an @-sign."
	}
	local, domain := s[:at], s[at+1:]

	if strings.HasPrefix(local, `"`) {
		return "", "Quoting the part before the @-sign is not allowed here."
	}
	if local == "" {
		return "", "There must be something before the @-sign."
	}
	var bad []string
	for _, r := range local {
		if r != '.' && !isAtext(r) {
			bad = append(bad, describeChar(r))
		}
	}
	if len(bad) > 0 {
		return "", "The email address contains invalid characters before the @-sign: " + joinUnique(bad) + "."
	}
	switch {
	case strings.HasPrefix(local, "."):
		return "", "An email address cannot start with a period."
	case strings.HasSuffix(local, "."):
		return "", "An email address cannot have a period immediately before the @-sign."
	case strings.Contains(local, ".."):
		return "", "An email address cannot have two periods in a row."
	}

	if domain == "" {
		return "", "There must be something after the @-sign."
	}
	if strings.HasPrefix(domain, "[") {
		return "", "A bracketed IP address after the @-sign is not allowed here."
	}
	domain = strings.ToLower(norm.NFKC.String(domain))
	bad = bad[:0]
	for _, r := range domain {
		ok := r == '.' || r == '-' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r >= 0x80 && (unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsNumber(r))
		if !ok {
			bad = append(bad, describeChar(r))
		}
	}
	if len(bad) > 0 {
		return "", "The part after the @-sign contains invalid characters: " + joinUnique(bad) + "."
	}
	switch {
	case strings.HasPrefix(domain, "."):
		return "", "An email address cannot have a period immediately after the @-sign."
	case strings.HasSuffix(domain, "."):
		return "", "An email address cannot end with a period."
	case strings.HasPrefix(domain, "-"):
		return "", "An email address cannot have a hyphen immediately after the @-sign."
	case strings.Contains(domain, ".."):
		return "", "An email address cannot have two periods in a row."
	case strings.Contains(domain, ".-") || strings.Contains(domain, "-."):
		return "", "An email address cannot have a period and a hyphen next to each other."
	}
	if n := utf8.RuneCountInString(domain); n > 253 {
		return "", "The email address is too long after the @-sign " + tooMany(n-253, "character", "characters") + "."
	}
	for _, label := range strings.Split(domain, ".") {
		if n := utf8.RuneCountInString(label); n > 63 {
			return "", "After the @-sign, periods cannot be separated by so many characters " + tooMany(n-63, "character", "characters") + "."
		}
	}
	if !strings.Contains(domain, ".") {
		return "", "The part after the @-sign is not valid. It should have a period."
	}
	labels := strings.Split(domain, ".")
	if tld := labels[len(labels)-1]; strings.Trim(tld, "0123456789") == "" {
		return "", "The part after the @-sign is not valid. It is not within a valid top-level domain."
	}
	for _, sp := range specialUseDomains {
		if domain == sp || strings.HasSuffix(domain, "."+sp) {
			return "", "The part after the @-sign is a special-use or reserved name that cannot be used with email."
		}
	}
	return norm.NFC.String(local) + "@" + domain, ""
}
