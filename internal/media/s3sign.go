package media

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// emptySHA is sha256(""), the payload hash of GET, HEAD and DELETE.
const emptySHA = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// sign adds an AWS SigV4 Authorization header. Only host and x-amz-*
// headers are signed; payload is the hex sha256 of the body, which S3 and
// R2 check against what arrives.
func sign(req *http.Request, accessKey, secretKey, region, payload string, now time.Time) {
	amzDate := now.UTC().Format("20060102T150405Z")
	day := amzDate[:8]
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payload)

	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	names := []string{"host"}
	vals := map[string]string{"host": host}
	for k, v := range req.Header {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-amz-") {
			names = append(names, lk)
			vals[lk] = strings.TrimSpace(strings.Join(v, ","))
		}
	}
	sort.Strings(names)
	var canon strings.Builder
	canon.WriteString(req.Method + "\n")
	canon.WriteString(req.URL.EscapedPath() + "\n")
	canon.WriteString(req.URL.RawQuery + "\n")
	for _, n := range names {
		canon.WriteString(n + ":" + vals[n] + "\n")
	}
	signed := strings.Join(names, ";")
	canon.WriteString("\n" + signed + "\n" + payload)

	scope := day + "/" + region + "/s3/aws4_request"
	sum := sha256.Sum256([]byte(canon.String()))
	sts := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	k := hmacSHA([]byte("AWS4"+secretKey), day)
	k = hmacSHA(k, region)
	k = hmacSHA(k, "s3")
	k = hmacSHA(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA(k, sts))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+accessKey+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}

func hmacSHA(key []byte, msg string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}

// canonQuery is the query string in SigV4 canonical form, which is also
// what goes on the wire so the two can't disagree.
func canonQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		for _, v := range q[k] {
			if b.Len() > 0 {
				b.WriteByte('&')
			}
			b.WriteString(awsEscape(k) + "=" + awsEscape(v))
		}
	}
	return b.String()
}

func awsEscape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}
