package s3

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// fakeCopyStore answers the requests of a multipart server-side copy and remembers how the upload was started.
type fakeCopyStore struct {
	mu      sync.Mutex
	created http.Header
	parts   []string
}

func (f *fakeCopyStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == "HEAD" && r.URL.Path == "/bkt/src.mp4":
		h := w.Header()
		h.Set("Content-Type", "video/mp4")
		h.Set("Content-Length", "10")
		h.Set("Cache-Control", "max-age=60")
		h.Set("x-amz-meta-title", "%E5%8A%A8%E7%94%BB") // stored percent-encoded, as metaHeaders writes it
	case r.Method == "POST" && q.Has("uploads"):
		f.created = r.Header.Clone()
		_, _ = w.Write([]byte(`<InitiateMultipartUploadResult><UploadId>u1</UploadId></InitiateMultipartUploadResult>`))
	case r.Method == "PUT" && q.Get("uploadId") == "u1":
		f.parts = append(f.parts, r.Header.Get("x-amz-copy-source-range"))
		_, _ = w.Write([]byte(`<CopyPartResult><ETag>"e1"</ETag></CopyPartResult>`))
	case r.Method == "POST" && q.Get("uploadId") == "u1":
		_, _ = w.Write([]byte(`<CompleteMultipartUploadResult><ETag>"x"</ETag></CompleteMultipartUploadResult>`))
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.String(), 400)
	}
}

func TestBigCopyKeepsContentTypeAndMetadata(t *testing.T) {
	store := &fakeCopyStore{}
	srv := httptest.NewServer(store)
	defer srv.Close()
	c, err := New(Connection{Endpoint: srv.URL, AccessKey: "k", SecretKey: "s", Addressing: "path"})
	if err != nil {
		t.Fatal(err)
	}
	defer func(old int64) { copyLimit = old }(copyLimit)
	copyLimit = 4 // anything bigger goes part by part

	if err := c.CopyObject("bkt", "src.mp4", "bkt", "dst.mp4", 10, "", nil, false, nil); err != nil {
		t.Fatal(err)
	}
	h := store.created
	if h == nil {
		t.Fatal("no multipart upload was started")
	}
	for name, want := range map[string]string{"Content-Type": "video/mp4", "Cache-Control": "max-age=60", "X-Amz-Meta-Title": "%E5%8A%A8%E7%94%BB"} {
		if got := h.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if h.Get("x-amz-metadata-directive") != "" {
		t.Error("the upload carried a copy request's header")
	}
	if len(store.parts) != 1 || store.parts[0] != "bytes=0-9" {
		t.Errorf("parts %v", store.parts)
	}

	// With new properties (SetProperties on a big object) those win over the source's.
	store.created = nil
	if err := c.CopyObject("bkt", "src.mp4", "bkt", "dst.mp4", 10, "video/quicktime", map[string]string{"a": "b"}, true, nil); err != nil {
		t.Fatal(err)
	}
	if got := store.created.Get("Content-Type"); got != "video/quicktime" || store.created.Get("X-Amz-Meta-A") != "b" || store.created.Get("X-Amz-Meta-Title") != "" {
		t.Errorf("replace: %v", store.created)
	}
}
