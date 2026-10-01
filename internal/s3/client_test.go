package s3

import (
	"strings"
	"testing"
	"time"
)

const (
	awsKey    = "AKIAIOSFODNN7EXAMPLE"
	awsSecret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
)

func awsClient(t *testing.T) *Client {
	t.Helper()
	c, err := New(Connection{Endpoint: "https://s3.amazonaws.com", AccessKey: awsKey, SecretKey: awsSecret, Addressing: "virtual", VerifyTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC) }
	return c
}

// https://docs.aws.amazon.com/AmazonS3/latest/API/sigv4-query-string-auth.html
func TestPresignMatchesTheAWSDocumentationExample(t *testing.T) {
	u, err := awsClient(t).Presign("GET", "examplebucket", "test.txt", 86400, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u, "https://examplebucket.s3.amazonaws.com/test.txt?") {
		t.Errorf("unexpected URL %s", u)
	}
	if !strings.HasSuffix(u, "X-Amz-Signature=aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404") {
		t.Errorf("wrong signature in %s", u)
	}
}

// GET Object with a Range header, from "Signature Calculations for the Authorization Header".
func TestHeaderSignatureMatchesTheAWSDocumentationExample(t *testing.T) {
	c := awsClient(t)
	key := "test.txt"
	host, path := c.target("examplebucket", &key)
	h := c.signedHeaders("GET", host, path, nil, map[string]string{"Range": "bytes=0-9"}, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
	if !strings.Contains(h["authorization"], "Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41") {
		t.Errorf("wrong signature: %s", h["authorization"])
	}
}

func TestPathStyleAndSpecialCharacters(t *testing.T) {
	c, _ := New(Connection{Endpoint: "http://127.0.0.1:9000", AccessKey: "a", SecretKey: "b"})
	key := "a b/é+&.mp4"
	host, path := c.target("media", &key)
	if host != "127.0.0.1:9000" || path != "/media/a%20b/%C3%A9%2B%26.mp4" {
		t.Errorf("got %s %s", host, path)
	}
	host, path = c.target("", nil)
	if host != "127.0.0.1:9000" || path != "/" {
		t.Errorf("got %s %s", host, path)
	}
}

func TestVirtualStyleFallsBackToPathForIPHosts(t *testing.T) {
	c, _ := New(Connection{Endpoint: "http://10.0.0.5:9000", AccessKey: "a", SecretKey: "b", Addressing: "virtual"})
	if c.virtual {
		t.Error("an IP address cannot be virtual-hosted")
	}
	auto, _ := New(Connection{Endpoint: "https://oss-cn-hangzhou.aliyuncs.com", AccessKey: "a", SecretKey: "b", Addressing: "auto"})
	if !auto.virtual {
		t.Error("auto should pick virtual style for aliyuncs.com")
	}
}

func TestEndpointParsing(t *testing.T) {
	cases := []struct{ in, region, scheme, host, base string }{
		{"fsapi.example.com", "", "https", "fsapi.example.com", ""},
		{"https://x.example.com:443/", "", "https", "x.example.com", ""},
		{"http://nas:9000/s3/", "", "http", "nas:9000", "/s3"},
		{"", "eu-west-1", "https", "s3.eu-west-1.amazonaws.com", ""},
		{"https://s3.{region}.example.com", "eu-1", "https", "s3.eu-1.example.com", ""},
	}
	for _, c := range cases {
		scheme, host, base, err := ParseEndpoint(c.in, c.region)
		if err != nil || scheme != c.scheme || host != c.host || base != c.base {
			t.Errorf("%q: got %s %s %s %v", c.in, scheme, host, base, err)
		}
	}
	if _, _, _, err := ParseEndpoint("https://", ""); err == nil {
		t.Error("an empty host must be refused")
	}
}

func TestKeyPathKeepsSlashesOnly(t *testing.T) {
	if got := KeyPath("a/b c/~d_e-f.g"); got != "a/b%20c/~d_e-f.g" {
		t.Errorf("got %s", got)
	}
}

func TestPartPlanningStaysUnderTenThousandParts(t *testing.T) {
	part, count := PlanParts(5<<40, 16<<20)
	if count > MaxParts || part < MinPart {
		t.Errorf("part %d count %d", part, count)
	}
	if p, n := PlanParts(1, 16<<20); p != 16<<20 || n != 1 {
		t.Errorf("got %d %d", p, n)
	}
}

func TestErrorParsing(t *testing.T) {
	e := parseError(404, nil2h(), []byte(`<?xml version="1.0"?><Error><Code>NoSuchKey</Code><Message>The key does not exist</Message><RequestId>R1</RequestId></Error>`), "GET")
	if e.Code != "NoSuchKey" || e.RequestID != "R1" || e.Retryable || e.Error() != "NoSuchKey: The key does not exist" {
		t.Errorf("%+v / %s", e, e)
	}
	if e := parseError(503, nil2h(), nil, "GET"); !e.Retryable || e.Code != "SlowDown" {
		t.Errorf("%+v", e)
	}
	if e := parseError(404, nil2h(), nil, "HEAD"); e.Message != "Not found" {
		t.Errorf("%+v", e)
	}
}

func nil2h() map[string][]string { return map[string][]string{} }
