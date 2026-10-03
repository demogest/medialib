// Package s3 is a small S3 client: AWS Signature V4 over net/http, standard library only.
//
// It talks to AWS S3 and to anything that speaks its API: RustFS, MinIO, Ceph, Cloudflare R2, Backblaze B2, Wasabi,
// Alibaba OSS, Tencent COS, DigitalOcean Spaces, Google Cloud Storage (interoperability mode) and so on.
package s3

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	emptySHA = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	unsigned = "UNSIGNED-PAYLOAD"
	MinPart  = 5 * 1024 * 1024 // S3 refuses smaller parts (except the last)
	MaxParts = 10_000
	maxTries = 4
)

// copyLimit is the size above which a server-side copy goes part by part (a single copy request allows 5 GiB).
var copyLimit int64 = 4 << 30

// Provider is a preset for the connection form. {region} in Endpoint is substituted.
type Provider struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	Endpoint   string `json:"endpoint"`
	Region     string `json:"region"`
	Addressing string `json:"addressing"`
}

// Providers in display order.
var Providers = []Provider{
	{"rustfs", "RustFS", "", "us-east-1", "path"},
	{"minio", "MinIO", "", "us-east-1", "path"},
	{"aws", "Amazon S3", "https://s3.{region}.amazonaws.com", "us-east-1", "virtual"},
	{"r2", "Cloudflare R2", "https://<account-id>.r2.cloudflarestorage.com", "auto", "path"},
	{"b2", "Backblaze B2", "https://s3.{region}.backblazeb2.com", "us-west-004", "path"},
	{"wasabi", "Wasabi", "https://s3.{region}.wasabisys.com", "us-east-1", "path"},
	{"oss", "Alibaba Cloud OSS", "https://oss-{region}.aliyuncs.com", "cn-hangzhou", "virtual"},
	{"cos", "Tencent COS", "https://cos.{region}.myqcloud.com", "ap-guangzhou", "virtual"},
	{"spaces", "DigitalOcean Spaces", "https://{region}.digitaloceanspaces.com", "nyc3", "path"},
	{"gcs", "Google Cloud Storage", "https://storage.googleapis.com", "auto", "path"},
	{"other", "Other S3-compatible", "", "us-east-1", "path"},
}

// ProviderByID returns the preset for id.
func ProviderByID(id string) (Provider, bool) {
	for _, p := range Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// Error is an error answer from the store (or a failure to reach it). Status 0 means no HTTP answer.
type Error struct {
	Status    int
	Code      string
	Message   string
	RequestID string
	Retryable bool
	Extra     map[string]string
}

func (e *Error) Error() string {
	head := ""
	if e.Code != "" && e.Code != e.Message {
		head = e.Code + ": "
	}
	switch {
	case e.Message != "":
		return head + e.Message
	case e.Code != "":
		return e.Code
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

// AsError returns err as an *Error when it is one.
func AsError(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

// IsStatus reports whether err is a store answer with that HTTP status.
func IsStatus(err error, status int) bool {
	e, ok := AsError(err)
	return ok && e.Status == status
}

// Connection says how to reach one store. Addressing: "path" (host/bucket/key), "virtual" (bucket.host/key) or "auto".
type Connection struct {
	Endpoint     string
	AccessKey    string
	SecretKey    string
	Region       string
	SessionToken string
	Addressing   string
	VerifyTLS    bool
	Timeout      time.Duration
}

// Quote percent-encodes everything but unreserved characters (plus any in safe).
func Quote(s string, safe ...string) string {
	keep := "-_.~"
	if len(safe) > 0 {
		keep += safe[0]
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(keep, c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// KeyPath encodes an object key for a URL path, keeping the slashes.
func KeyPath(key string) string { return Quote(key, "/") }

func hmacSHA(key []byte, msg string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// ParseEndpoint splits whatever the user typed into (scheme, host[:port], base path).
func ParseEndpoint(endpoint, region string) (scheme, host, base string, err error) {
	if region == "" {
		region = "us-east-1"
	}
	endpoint = strings.TrimSpace(strings.ReplaceAll(endpoint, "{region}", region))
	if endpoint == "" {
		endpoint = "https://s3." + region + ".amazonaws.com"
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	u, perr := url.Parse(endpoint)
	if perr != nil || u.Hostname() == "" {
		return "", "", "", fmt.Errorf("Not a valid endpoint: %s", endpoint)
	}
	host = u.Hostname()
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if p := u.Port(); p != "" && !((u.Scheme == "https" && p == "443") || (u.Scheme == "http" && p == "80")) {
		host += ":" + p
	}
	return u.Scheme, host, strings.TrimRight(u.Path, "/"), nil
}

// ISO is the second-precision UTC timestamp the library index stores ("2026-09-29T11:37:57Z").
func ISO(ts string) string {
	if ts == "" {
		return ""
	}
	if len(ts) > 19 {
		ts = ts[:19]
	}
	return ts + "Z"
}

// Client is safe for concurrent use.
type Client struct {
	conn    Connection
	scheme  string
	netloc  string
	base    string
	virtual bool
	mu      sync.RWMutex
	region  string
	http    *http.Client
	now     func() time.Time // a test seam: signatures depend on the clock
}

var hostIsAddress = regexp.MustCompile(`^([\d.]+|\[.*\]|localhost)$`)
var virtualHosts = regexp.MustCompile(`(amazonaws\.com|aliyuncs\.com|myqcloud\.com)$`)

// New builds a client. It fails only on an unparseable endpoint.
func New(conn Connection) (*Client, error) {
	region := conn.Region
	if region == "" {
		region = "us-east-1"
	}
	scheme, netloc, base, err := ParseEndpoint(conn.Endpoint, region)
	if err != nil {
		return nil, err
	}
	host := netloc
	if i := strings.LastIndex(host, ":"); i > 0 && !strings.HasSuffix(host, "]") {
		host = host[:i]
	}
	host = strings.Trim(host, "[]")
	mode := conn.Addressing
	if mode == "" {
		mode = "path"
	}
	if mode == "auto" {
		mode = "path"
		if virtualHosts.MatchString(host) {
			mode = "virtual"
		}
	}
	timeout := conn.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          128,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: timeout,
		ForceAttemptHTTP2:     false,
	}
	if scheme == "https" && !conn.VerifyTLS {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // the user switched verification off for this connection
	}
	return &Client{
		conn: conn, scheme: scheme, netloc: netloc, base: base, region: region,
		virtual: mode == "virtual" && !hostIsAddress.MatchString(host),
		http:    &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		now:     func() time.Time { return time.Now().UTC() },
	}, nil
}

// Close drops idle connections.
func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) getRegion() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.region
}

// ---------------------------------------------------------------- addressing and signing

func (c *Client) target(bucket string, key *string) (host, path string) {
	host, path = c.netloc, c.base+"/"
	switch {
	case bucket != "" && c.virtual:
		host = bucket + "." + c.netloc
		path = c.base + "/"
		if key != nil && *key != "" {
			path += KeyPath(*key)
		}
	case bucket != "":
		path = c.base + "/" + Quote(bucket)
		if key != nil {
			path += "/" + KeyPath(*key)
		}
	}
	return
}

func canonicalQuery(q url.Values) string {
	type kv struct{ k, v string }
	var all []kv
	for k, vs := range q {
		for _, v := range vs {
			all = append(all, kv{Quote(k), Quote(v)})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].k != all[j].k {
			return all[i].k < all[j].k
		}
		return all[i].v < all[j].v
	})
	parts := make([]string, len(all))
	for i, p := range all {
		parts[i] = p.k + "=" + p.v
	}
	return strings.Join(parts, "&")
}

func (c *Client) signingKey(date, region string) []byte {
	k := hmacSHA([]byte("AWS4"+c.conn.SecretKey), date)
	for _, part := range []string{region, "s3", "aws4_request"} {
		k = hmacSHA(k, part)
	}
	return k
}

// signedHeaders returns the headers to send (lower-case names, no Host) for a request.
func (c *Client) signedHeaders(method, host, path string, query url.Values, headers map[string]string, payloadHash string) map[string]string {
	now := c.now()
	amzDate, date := now.Format("20060102T150405Z"), now.Format("20060102")
	region := c.getRegion()
	h := map[string]string{}
	for k, v := range headers {
		h[strings.ToLower(k)] = v
	}
	h["host"], h["x-amz-date"], h["x-amz-content-sha256"] = host, amzDate, payloadHash
	if c.conn.SessionToken != "" {
		h["x-amz-security-token"] = c.conn.SessionToken
	}
	names := make([]string, 0, len(h))
	for k := range h {
		names = append(names, k)
	}
	sort.Strings(names)
	var canon strings.Builder
	canon.WriteString(method + "\n" + path + "\n" + canonicalQuery(query) + "\n")
	for _, k := range names {
		canon.WriteString(k + ":" + strings.Join(strings.Fields(h[k]), " ") + "\n")
	}
	signed := strings.Join(names, ";")
	canon.WriteString("\n" + signed + "\n" + payloadHash)
	scope := date + "/" + region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canon.String()))
	sig := hex.EncodeToString(hmacSHA(c.signingKey(date, region), toSign))
	h["authorization"] = fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s", c.conn.AccessKey, scope, signed, sig)
	delete(h, "host")
	return h
}

// Presign returns a URL that grants method on one object for expires seconds, with no credentials needed.
func (c *Client) Presign(method, bucket, key string, expires int, responseHeaders map[string]string) (string, error) {
	if expires < 1 || expires > 7*86400 {
		return "", errors.New("A presigned URL can be valid for 1 second to 7 days.")
	}
	host, path := c.target(bucket, &key)
	now := c.now()
	amzDate, date := now.Format("20060102T150405Z"), now.Format("20060102")
	region := c.getRegion()
	scope := date + "/" + region + "/s3/aws4_request"
	q := url.Values{}
	for k, v := range responseHeaders { // e.g. {"response-content-disposition": "attachment"}
		q.Set(k, v)
	}
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", c.conn.AccessKey+"/"+scope)
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", strconv.Itoa(expires))
	q.Set("X-Amz-SignedHeaders", "host")
	if c.conn.SessionToken != "" {
		q.Set("X-Amz-Security-Token", c.conn.SessionToken)
	}
	canon := strings.Join([]string{method, path, canonicalQuery(q), "host:" + host + "\n", "host", unsigned}, "\n")
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canon))
	sig := hex.EncodeToString(hmacSHA(c.signingKey(date, region), toSign))
	return fmt.Sprintf("%s://%s%s?%s&X-Amz-Signature=%s", c.scheme, host, path, canonicalQuery(q), sig), nil
}

// ---------------------------------------------------------------- transport

// Stream is a response body being read piece by piece. Close hands the connection back when it was read to the end.
type Stream struct {
	Status  int
	Headers http.Header
	Body    io.ReadCloser
}

func (s *Stream) Read(p []byte) (int, error) { return s.Body.Read(p) }
func (s *Stream) Close() error               { return s.Body.Close() }

type reqSpec struct {
	method, bucket string
	key            *string
	query          url.Values
	headers        map[string]string
	body           []byte
	stream         bool
	ctx            context.Context
}

func (c *Client) once(r reqSpec) (int, http.Header, []byte, *Stream, error) {
	host, path := c.target(r.bucket, r.key)
	payloadHash := emptySHA
	if len(r.body) > 0 {
		payloadHash = sha256Hex(r.body)
	}
	hdrs := c.signedHeaders(r.method, host, path, r.query, r.headers, payloadHash)
	target := c.scheme + "://" + host + path
	if len(r.query) > 0 {
		target += "?" + canonicalQuery(r.query)
	}
	ctx := r.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	var body io.Reader
	if len(r.body) > 0 {
		body = bytes.NewReader(r.body)
	}
	req, err := http.NewRequestWithContext(ctx, r.method, target, body)
	if err != nil {
		return 0, nil, nil, nil, &Error{Code: "BadRequest", Message: err.Error()}
	}
	// The URL is already in canonical form; keep its escaping as signed.
	if u, perr := url.Parse(target); perr == nil {
		req.URL = u
		req.URL.RawPath = path
	}
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	if len(r.body) > 0 || r.method == "PUT" || r.method == "POST" {
		req.ContentLength = int64(len(r.body))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, nil, nil, ctx.Err()
		}
		return 0, nil, nil, nil, &Error{Code: "ConnectionError", Message: fmt.Sprintf("Cannot reach %s: %v", host, shortErr(err)), Retryable: true}
	}
	if resp.StatusCode >= 400 || !r.stream {
		var data []byte
		if r.method != "HEAD" {
			data, err = io.ReadAll(resp.Body)
		}
		resp.Body.Close()
		if err != nil {
			return 0, nil, nil, nil, &Error{Code: "ConnectionError", Message: fmt.Sprintf("Connection to %s dropped: %v", host, shortErr(err)), Retryable: true}
		}
		if resp.StatusCode >= 400 {
			return 0, nil, nil, nil, parseError(resp.StatusCode, resp.Header, data, r.method)
		}
		return resp.StatusCode, resp.Header, data, nil, nil
	}
	return resp.StatusCode, resp.Header, nil, &Stream{Status: resp.StatusCode, Headers: resp.Header, Body: resp.Body}, nil
}

func shortErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return err.Error()
}

type errorXML struct {
	Code      string `xml:"Code"`
	Message   string `xml:"Message"`
	RequestID string `xml:"RequestId"`
	Region    string `xml:"Region"`
	Endpoint  string `xml:"Endpoint"`
	Bucket    string `xml:"BucketName"`
	Key       string `xml:"Key"`
}

func parseError(status int, h http.Header, data []byte, method string) *Error {
	e := &Error{Status: status, RequestID: h.Get("x-amz-request-id"), Extra: map[string]string{}}
	if t := bytes.TrimSpace(data); len(t) > 0 && t[0] == '<' {
		var x errorXML
		if xml.Unmarshal(data, &x) == nil {
			e.Code, e.Message = x.Code, x.Message
			if x.RequestID != "" {
				e.RequestID = x.RequestID
			}
			for k, v := range map[string]string{"Region": x.Region, "Endpoint": x.Endpoint, "BucketName": x.Bucket, "Key": x.Key} {
				if v != "" {
					e.Extra[k] = v
				}
			}
		}
	}
	if e.Code == "" {
		codes := map[int]string{301: "PermanentRedirect", 400: "BadRequest", 403: "AccessDenied", 404: "NotFound", 409: "Conflict",
			412: "PreconditionFailed", 416: "InvalidRange", 503: "SlowDown"}
		e.Code = codes[status]
		if e.Code == "" {
			e.Code = fmt.Sprintf("HTTP%d", status)
		}
		if method == "HEAD" && e.Message == "" {
			e.Message = map[int]string{403: "Access denied", 404: "Not found"}[status]
		}
	}
	if e.Message == "" {
		e.Message = e.Code
	}
	switch status {
	case 500, 502, 503, 504:
		e.Retryable = true
	}
	switch e.Code {
	case "SlowDown", "RequestTimeout", "InternalError":
		e.Retryable = true
	}
	return e
}

func (c *Client) do(r reqSpec) (int, http.Header, []byte, *Stream, error) {
	var last error
	for attempt := 0; attempt < maxTries; attempt++ {
		status, h, data, st, err := c.once(r)
		if err == nil {
			return status, h, data, st, nil
		}
		last = err
		e, ok := AsError(err)
		if !ok {
			return 0, nil, nil, nil, err
		}
		// A store in another region names it in the error: take its word for it and sign again.
		if hint := e.Extra["Region"]; hint != "" && hint != c.getRegion() {
			switch e.Code {
			case "AuthorizationHeaderMalformed", "IllegalLocationConstraintException", "PermanentRedirect":
				c.mu.Lock()
				c.region = hint
				c.mu.Unlock()
				continue
			}
		}
		if !e.Retryable || attempt == maxTries-1 {
			return 0, nil, nil, nil, err
		}
		select {
		case <-time.After(time.Duration(400*(1<<attempt)) * time.Millisecond):
		case <-func() <-chan struct{} {
			if r.ctx != nil {
				return r.ctx.Done()
			}
			return nil
		}():
			return 0, nil, nil, nil, r.ctx.Err()
		}
	}
	return 0, nil, nil, nil, last
}

func (c *Client) request(method, bucket string, key *string, query url.Values, headers map[string]string, body []byte) (http.Header, []byte, error) {
	_, h, data, _, err := c.do(reqSpec{method: method, bucket: bucket, key: key, query: query, headers: headers, body: body})
	return h, data, err
}

func ptr(s string) *string { return &s }

func q(kv ...string) url.Values {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return v
}

// ---------------------------------------------------------------- buckets

type Bucket struct {
	Name    string `json:"name"`
	Created string `json:"created"`
}

func (c *Client) ListBuckets() ([]Bucket, error) {
	_, data, err := c.request("GET", "", nil, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	var x struct {
		Buckets []struct {
			Name         string `xml:"Name"`
			CreationDate string `xml:"CreationDate"`
		} `xml:"Buckets>Bucket"`
	}
	if err := xml.Unmarshal(data, &x); err != nil {
		return nil, &Error{Code: "BadResponse", Message: "Unreadable bucket list: " + err.Error()}
	}
	out := make([]Bucket, 0, len(x.Buckets))
	for _, b := range x.Buckets {
		out = append(out, Bucket{b.Name, ISO(b.CreationDate)})
	}
	return out, nil
}

func (c *Client) CreateBucket(bucket string) error {
	var body []byte
	if r := c.getRegion(); r != "us-east-1" && r != "auto" && r != "" {
		body = []byte("<CreateBucketConfiguration><LocationConstraint>" + r + "</LocationConstraint></CreateBucketConfiguration>")
	}
	_, _, err := c.request("PUT", bucket, nil, nil, nil, body)
	return err
}

func (c *Client) DeleteBucket(bucket string) error {
	_, _, err := c.request("DELETE", bucket, nil, nil, nil, nil)
	return err
}

// ---------------------------------------------------------------- listing

type Object struct {
	Key          string `json:"key"`
	Size         int64  `json:"size"`
	MTime        string `json:"mtime"`
	ETag         string `json:"etag"`
	StorageClass string `json:"storage_class"`
}

// Page is one page of ListObjectsV2.
type Page struct {
	Prefixes []string `json:"prefixes"`
	Objects  []Object `json:"objects"`
	Next     *string  `json:"next"`
}

type listXML struct {
	EncodingType string `xml:"EncodingType"`
	IsTruncated  string `xml:"IsTruncated"`
	NextToken    string `xml:"NextContinuationToken"`
	Contents     []struct {
		Key          string `xml:"Key"`
		Size         string `xml:"Size"`
		LastModified string `xml:"LastModified"`
		ETag         string `xml:"ETag"`
		StorageClass string `xml:"StorageClass"`
	} `xml:"Contents"`
	CommonPrefixes []struct {
		Prefix string `xml:"Prefix"`
	} `xml:"CommonPrefixes"`
}

func (c *Client) ListObjects(bucket, prefix, delimiter, token string, maxKeys int) (*Page, error) {
	if maxKeys <= 0 {
		maxKeys = 1000
	}
	query := q("list-type", "2", "max-keys", strconv.Itoa(maxKeys), "encoding-type", "url")
	if prefix != "" {
		query.Set("prefix", prefix)
	}
	if delimiter != "" {
		query.Set("delimiter", delimiter)
	}
	if token != "" {
		query.Set("continuation-token", token)
	}
	_, data, err := c.request("GET", bucket, nil, query, nil, nil)
	if err != nil {
		return nil, err
	}
	var x listXML
	if err := xml.Unmarshal(data, &x); err != nil {
		return nil, &Error{Code: "BadResponse", Message: "Unreadable listing: " + err.Error()}
	}
	dec := func(s string) string { return s }
	if x.EncodingType == "url" {
		dec = func(s string) string {
			if u, err := url.QueryUnescape(s); err == nil { // S3 writes a space as "+" and a plus sign as %2B
				return u
			}
			return s
		}
	}
	page := &Page{Prefixes: []string{}, Objects: make([]Object, 0, len(x.Contents))}
	for _, o := range x.Contents {
		size, _ := strconv.ParseInt(o.Size, 10, 64)
		sc := o.StorageClass
		if sc == "" {
			sc = "STANDARD"
		}
		page.Objects = append(page.Objects, Object{dec(o.Key), size, ISO(o.LastModified), strings.Trim(o.ETag, `"`), sc})
	}
	for _, p := range x.CommonPrefixes {
		page.Prefixes = append(page.Prefixes, dec(p.Prefix))
	}
	if x.IsTruncated == "true" && x.NextToken != "" {
		t := x.NextToken
		page.Next = &t
	}
	return page, nil
}

// EachObject calls fn for every object under a prefix, page after page. fn returning an error stops the walk.
func (c *Client) EachObject(bucket, prefix, delimiter string, fn func(Object) error) error {
	token := ""
	for {
		page, err := c.ListObjects(bucket, prefix, delimiter, token, 1000)
		if err != nil {
			return err
		}
		for _, o := range page.Objects {
			if err := fn(o); err != nil {
				return err
			}
		}
		if page.Next == nil {
			return nil
		}
		token = *page.Next
	}
}

// Upload is a multipart upload that was started and never finished (it still takes up space).
type Upload struct {
	Key      string `json:"key"`
	UploadID string `json:"upload_id"`
	Started  string `json:"started"`
}

func (c *Client) ListUploads(bucket, prefix string) ([]Upload, error) {
	out := []Upload{}
	marker, uploadMarker := "", ""
	for {
		query := q("uploads", "", "encoding-type", "url")
		if prefix != "" {
			query.Set("prefix", prefix)
		}
		if marker != "" {
			query.Set("key-marker", marker)
			query.Set("upload-id-marker", uploadMarker)
		}
		_, data, err := c.request("GET", bucket, nil, query, nil, nil)
		if err != nil {
			return nil, err
		}
		var x struct {
			IsTruncated    string `xml:"IsTruncated"`
			NextKeyMarker  string `xml:"NextKeyMarker"`
			NextUploadMark string `xml:"NextUploadIdMarker"`
			Uploads        []struct {
				Key       string `xml:"Key"`
				UploadID  string `xml:"UploadId"`
				Initiated string `xml:"Initiated"`
			} `xml:"Upload"`
		}
		if err := xml.Unmarshal(data, &x); err != nil {
			return nil, &Error{Code: "BadResponse", Message: err.Error()}
		}
		for _, u := range x.Uploads {
			k, _ := url.QueryUnescape(u.Key)
			out = append(out, Upload{k, u.UploadID, ISO(u.Initiated)})
		}
		if x.IsTruncated != "true" {
			return out, nil
		}
		marker, _ = url.QueryUnescape(x.NextKeyMarker)
		uploadMarker = x.NextUploadMark
	}
}

// ---------------------------------------------------------------- objects

// Info is what HEAD says about an object.
type Info struct {
	Size               int64             `json:"size"`
	MTime              string            `json:"mtime"`
	ETag               string            `json:"etag"`
	ContentType        string            `json:"content_type"`
	CacheControl       string            `json:"cache_control"`
	ContentDisposition string            `json:"content_disposition"`
	ContentEncoding    string            `json:"content_encoding"`
	StorageClass       string            `json:"storage_class"`
	VersionID          string            `json:"version_id"`
	Metadata           map[string]string `json:"metadata"`
	GuessedType        string            `json:"guessed_type,omitempty"`
}

func objectInfo(h http.Header) *Info {
	meta := map[string]string{}
	for k, vs := range h {
		if lk := strings.ToLower(k); strings.HasPrefix(lk, "x-amz-meta-") && len(vs) > 0 {
			v, err := url.PathUnescape(vs[0])
			if err != nil {
				v = vs[0]
			}
			meta[lk[len("x-amz-meta-"):]] = v
		}
	}
	size, _ := strconv.ParseInt(h.Get("Content-Length"), 10, 64)
	if cr := h.Get("Content-Range"); strings.Contains(cr, "/") {
		if n, err := strconv.ParseInt(cr[strings.LastIndex(cr, "/")+1:], 10, 64); err == nil {
			size = n
		}
	}
	mtime := h.Get("Last-Modified")
	if t, err := time.Parse(time.RFC1123, mtime); err == nil {
		mtime = t.UTC().Format("2006-01-02T15:04:05Z")
	}
	sc := h.Get("x-amz-storage-class")
	if sc == "" {
		sc = "STANDARD"
	}
	return &Info{Size: size, MTime: mtime, ETag: strings.Trim(h.Get("ETag"), `"`), ContentType: h.Get("Content-Type"),
		CacheControl: h.Get("Cache-Control"), ContentDisposition: h.Get("Content-Disposition"),
		ContentEncoding: h.Get("Content-Encoding"), StorageClass: sc, VersionID: h.Get("x-amz-version-id"), Metadata: meta}
}

func (c *Client) HeadObject(bucket, key string) (*Info, error) {
	h, _, err := c.request("HEAD", bucket, &key, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	return objectInfo(h), nil
}

// GetObject reads a whole object, or with a non-empty byteRange ("bytes=0-99") part of it.
func (c *Client) GetObject(bucket, key, byteRange string) ([]byte, error) {
	var hdrs map[string]string
	if byteRange != "" {
		hdrs = map[string]string{"Range": byteRange}
	}
	_, data, err := c.request("GET", bucket, &key, nil, hdrs, nil)
	return data, err
}

// GetRange reads bytes start..end (inclusive), never reading past end: a store that ignores the Range header is an
// error rather than a reason to download a whole object.
func (c *Client) GetRange(bucket, key string, start, end int64) ([]byte, error) {
	want := end - start + 1
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 1500 * time.Millisecond)
		}
		st, err := c.GetObjectStream(context.Background(), bucket, key, fmt.Sprintf("bytes=%d-%d", start, end))
		if err != nil {
			return nil, err
		}
		if st.Status != 206 {
			n, _ := strconv.ParseInt(st.Headers.Get("Content-Length"), 10, 64)
			if !(st.Status == 200 && start == 0 && n > 0 && n <= want) {
				st.Close()
				return nil, &Error{Status: st.Status, Code: "RangeIgnored", Message: "server ignored the Range header"}
			}
		}
		data, err := io.ReadAll(io.LimitReader(st.Body, want))
		st.Close()
		if err == nil {
			return data, nil
		}
		last = &Error{Code: "ConnectionError", Message: "Connection dropped: " + shortErr(err), Retryable: true}
	}
	return nil, last
}

// GetObjectStream opens an object for streaming; check Status (200 or 206). The caller closes it.
func (c *Client) GetObjectStream(ctx context.Context, bucket, key, byteRange string) (*Stream, error) {
	var hdrs map[string]string
	if byteRange != "" {
		hdrs = map[string]string{"Range": byteRange}
	}
	_, _, _, st, err := c.do(reqSpec{method: "GET", bucket: bucket, key: &key, headers: hdrs, stream: true, ctx: ctx})
	return st, err
}

func metaHeaders(contentType string, metadata map[string]string, extra map[string]string) map[string]string {
	h := map[string]string{}
	for k, v := range extra {
		h[k] = v
	}
	if contentType != "" {
		h["Content-Type"] = contentType
	}
	for k, v := range metadata {
		ascii := true
		for i := 0; i < len(v); i++ {
			if v[i] >= 0x80 {
				ascii = false
				break
			}
		}
		if !ascii {
			v = Quote(v) // headers are ASCII only: stored percent-encoded, decoded again on the way out
		}
		h["x-amz-meta-"+strings.ToLower(k)] = v
	}
	return h
}

func (c *Client) PutObject(bucket, key string, data []byte, contentType string, metadata, headers map[string]string) (string, error) {
	h, _, err := c.request("PUT", bucket, &key, nil, metaHeaders(contentType, metadata, headers), data)
	if err != nil {
		return "", err
	}
	return strings.Trim(h.Get("ETag"), `"`), nil
}

func (c *Client) DeleteObject(bucket, key string) error {
	_, _, err := c.request("DELETE", bucket, &key, nil, nil, nil)
	return err
}

// DeleteFailure is one key a bulk delete could not remove.
type DeleteFailure struct{ Key, Code, Message string }

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// DeleteObjects removes keys, up to 1000 per call. It returns what was deleted and what failed.
func (c *Client) DeleteObjects(bucket string, keys []string) (deleted []string, failed []DeleteFailure, err error) {
	for i := 0; i < len(keys); i += 1000 {
		batch := keys[i:min(i+1000, len(keys))]
		var b strings.Builder
		b.WriteString("<Delete><Quiet>false</Quiet>")
		for _, k := range batch {
			b.WriteString("<Object><Key>" + xmlEscape(k) + "</Key></Object>")
		}
		b.WriteString("</Delete>")
		body := []byte(b.String())
		sum := md5.Sum(body) //nolint:gosec // S3 insists on a Content-MD5 here
		_, data, rerr := c.request("POST", bucket, nil, q("delete", ""), map[string]string{
			"Content-MD5": base64.StdEncoding.EncodeToString(sum[:]), "Content-Type": "application/xml"}, body)
		if rerr != nil {
			return deleted, failed, rerr
		}
		var x struct {
			Deleted []struct {
				Key string `xml:"Key"`
			} `xml:"Deleted"`
			Errors []struct {
				Key     string `xml:"Key"`
				Code    string `xml:"Code"`
				Message string `xml:"Message"`
			} `xml:"Error"`
		}
		_ = xml.Unmarshal(data, &x)
		for _, d := range x.Deleted {
			deleted = append(deleted, d.Key)
		}
		for _, e := range x.Errors {
			failed = append(failed, DeleteFailure{e.Key, e.Code, e.Message})
		}
	}
	return deleted, failed, nil
}

func copySource(bucket, key string) string { return "/" + Quote(bucket) + "/" + KeyPath(key) }

func bodyError(data []byte) error {
	if t := bytes.TrimSpace(data); len(t) > 0 && t[0] == '<' {
		var x errorXML
		if xml.Unmarshal(data, &x) == nil && x.Code != "" && bytes.Contains(t[:min(len(t), 64)], []byte("<Error")) {
			return &Error{Status: 500, Code: x.Code, Message: x.Message}
		}
	}
	return nil
}

// CopyObject copies server side. With replace the new content type and metadata replace the source's (also in place).
func (c *Client) CopyObject(srcBucket, srcKey, bucket, key string, size int64, contentType string, metadata map[string]string, replace bool, headers map[string]string) error {
	hdrs := metaHeaders(contentType, metadata, headers)
	hdrs["x-amz-copy-source"] = copySource(srcBucket, srcKey)
	if replace {
		hdrs["x-amz-metadata-directive"] = "REPLACE"
	}
	if size > copyLimit {
		if !replace {
			// A copy made part by part is a new upload: unlike a single copy request it takes nothing from the source
			// by itself, so its content type, headers and metadata are carried over here.
			info, err := c.HeadObject(srcBucket, srcKey)
			if err != nil {
				return err
			}
			extra := map[string]string{}
			for k, v := range map[string]string{"Cache-Control": info.CacheControl, "Content-Disposition": info.ContentDisposition,
				"Content-Encoding": info.ContentEncoding} {
				if v != "" {
					extra[k] = v
				}
			}
			for k, v := range headers {
				extra[k] = v
			}
			ctype := contentType
			if ctype == "" {
				ctype = info.ContentType
			}
			meta := metadata
			if meta == nil {
				meta = info.Metadata
			}
			hdrs = metaHeaders(ctype, meta, extra)
		}
		return c.copyMultipart(srcBucket, srcKey, bucket, key, size, hdrs)
	}
	_, data, err := c.request("PUT", bucket, &key, nil, hdrs, nil)
	if err != nil {
		return err
	}
	return bodyError(data) // S3 can answer 200 and then fail the copy in the body
}

func (c *Client) copyMultipart(srcBucket, srcKey, bucket, key string, size int64, hdrs map[string]string) error {
	delete(hdrs, "x-amz-copy-source")
	delete(hdrs, "x-amz-metadata-directive") // a copy request's header, not an upload's
	upload, err := c.CreateMultipart(bucket, key, "", nil, hdrs)
	if err != nil {
		return err
	}
	var parts []Part
	const step = 512 << 20
	for n, start := 1, int64(0); start < size; n, start = n+1, start+step {
		end := min(size, start+step) - 1
		_, data, err := c.request("PUT", bucket, &key, q("partNumber", strconv.Itoa(n), "uploadId", upload), map[string]string{
			"x-amz-copy-source": copySource(srcBucket, srcKey), "x-amz-copy-source-range": fmt.Sprintf("bytes=%d-%d", start, end)}, nil)
		if err == nil {
			err = bodyError(data)
		}
		if err != nil {
			c.AbortMultipart(bucket, key, upload)
			return err
		}
		var x struct {
			ETag string `xml:"ETag"`
		}
		_ = xml.Unmarshal(data, &x)
		parts = append(parts, Part{n, x.ETag})
	}
	if err := c.CompleteMultipart(bucket, key, upload, parts); err != nil {
		c.AbortMultipart(bucket, key, upload)
		return err
	}
	return nil
}

// ---------------------------------------------------------------- multipart upload

type Part struct {
	Number int
	ETag   string
}

func (c *Client) CreateMultipart(bucket, key, contentType string, metadata, headers map[string]string) (string, error) {
	_, data, err := c.request("POST", bucket, &key, q("uploads", ""), metaHeaders(contentType, metadata, headers), nil)
	if err != nil {
		return "", err
	}
	var x struct {
		UploadID string `xml:"UploadId"`
	}
	if err := xml.Unmarshal(data, &x); err != nil || x.UploadID == "" {
		return "", &Error{Code: "BadResponse", Message: "The store did not return an upload id."}
	}
	return x.UploadID, nil
}

func (c *Client) UploadPart(bucket, key, uploadID string, number int, data []byte) (string, error) {
	h, _, err := c.request("PUT", bucket, &key, q("partNumber", strconv.Itoa(number), "uploadId", uploadID), nil, data)
	if err != nil {
		return "", err
	}
	return h.Get("ETag"), nil
}

func (c *Client) CompleteMultipart(bucket, key, uploadID string, parts []Part) error {
	sort.Slice(parts, func(i, j int) bool { return parts[i].Number < parts[j].Number })
	var b strings.Builder
	b.WriteString("<CompleteMultipartUpload>")
	for _, p := range parts {
		fmt.Fprintf(&b, "<Part><PartNumber>%d</PartNumber><ETag>%s</ETag></Part>", p.Number, xmlEscape(p.ETag))
	}
	b.WriteString("</CompleteMultipartUpload>")
	_, data, err := c.request("POST", bucket, &key, q("uploadId", uploadID), map[string]string{"Content-Type": "application/xml"}, []byte(b.String()))
	if err != nil {
		return err
	}
	return bodyError(data)
}

func (c *Client) AbortMultipart(bucket, key, uploadID string) {
	_, _, _ = c.request("DELETE", bucket, &key, q("uploadId", uploadID), nil, nil)
}

// PlanParts returns (part size, count) so the object fits in 10,000 parts, parts being at least 5 MiB.
func PlanParts(size, partSize int64) (int64, int64) {
	part := max(MinPart, partSize)
	for size > part*MaxParts {
		part *= 2
	}
	n := (size + part - 1) / part
	return part, max(1, n)
}

// Ptr is a helper for callers building optional keys.
func Ptr(s string) *string { return ptr(s) }
