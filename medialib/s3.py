"""A small S3 client: AWS Signature V4 over http.client, standard library only.

Talks to AWS S3 and to anything that speaks its API: RustFS, MinIO, Ceph, Cloudflare R2, Backblaze B2, Wasabi,
Alibaba OSS, Tencent COS, DigitalOcean Spaces, Google Cloud Storage (interoperability mode) and so on.

    client = S3Client(Connection(endpoint="https://s3.example.com", access_key="...", secret_key="..."))
    for b in client.list_buckets(): ...
    page = client.list_objects("media", prefix="videos/", delimiter="/")
    url = client.presign("GET", "media", "videos/a.mp4", expires=3600)
"""
import base64
import hashlib
import hmac
import http.client
import re
import ssl
import threading
import time
import urllib.parse
import xml.etree.ElementTree as ET
from dataclasses import dataclass, field
from datetime import datetime, timezone

EMPTY_SHA = hashlib.sha256(b"").hexdigest()
UNSIGNED = "UNSIGNED-PAYLOAD"
MIN_PART = 5 * 1024 * 1024        # S3 refuses smaller parts (except the last)
MAX_PARTS = 10_000
RETRY_STATUS = {500, 502, 503, 504}

# Presets for the connection form: what to fill in for each kind of store. {region} is substituted.
PROVIDERS = {
    "rustfs": {"label": "RustFS", "endpoint": "", "region": "us-east-1", "addressing": "path"},
    "minio": {"label": "MinIO", "endpoint": "", "region": "us-east-1", "addressing": "path"},
    "aws": {"label": "Amazon S3", "endpoint": "https://s3.{region}.amazonaws.com", "region": "us-east-1", "addressing": "virtual"},
    "r2": {"label": "Cloudflare R2", "endpoint": "https://<account-id>.r2.cloudflarestorage.com", "region": "auto", "addressing": "path"},
    "b2": {"label": "Backblaze B2", "endpoint": "https://s3.{region}.backblazeb2.com", "region": "us-west-004", "addressing": "path"},
    "wasabi": {"label": "Wasabi", "endpoint": "https://s3.{region}.wasabisys.com", "region": "us-east-1", "addressing": "path"},
    "oss": {"label": "Alibaba Cloud OSS", "endpoint": "https://oss-{region}.aliyuncs.com", "region": "cn-hangzhou", "addressing": "virtual"},
    "cos": {"label": "Tencent COS", "endpoint": "https://cos.{region}.myqcloud.com", "region": "ap-guangzhou", "addressing": "virtual"},
    "spaces": {"label": "DigitalOcean Spaces", "endpoint": "https://{region}.digitaloceanspaces.com", "region": "nyc3", "addressing": "path"},
    "gcs": {"label": "Google Cloud Storage", "endpoint": "https://storage.googleapis.com", "region": "auto", "addressing": "path"},
    "other": {"label": "Other S3-compatible", "endpoint": "", "region": "us-east-1", "addressing": "path"},
}


class S3Error(Exception):
    """An error answer from the store (or a failure to reach it). status 0 means no HTTP answer."""

    def __init__(self, status, code, message, request_id=None, retryable=False):
        super().__init__(message or code)
        self.status, self.code, self.message, self.request_id, self.retryable = status, code, message, request_id, retryable

    def to_dict(self):
        return {"status": self.status, "code": self.code, "message": self.message, "request_id": self.request_id}

    def __str__(self):
        head = f"{self.code}: " if self.code and self.code != self.message else ""
        return f"{head}{self.message}" if self.message else self.code or f"HTTP {self.status}"


@dataclass
class Connection:
    """How to reach one store. addressing: "path" (host/bucket/key), "virtual" (bucket.host/key) or "auto"."""
    endpoint: str = ""
    access_key: str = ""
    secret_key: str = ""
    region: str = "us-east-1"
    session_token: str = ""
    addressing: str = "path"
    verify_tls: bool = True
    timeout: float = 60.0
    extra: dict = field(default_factory=dict)


def quote(text, safe="-_.~"):
    return urllib.parse.quote(text, safe=safe)


def key_path(key):
    return quote(key, "/-_.~")


def _hmac(key, msg):
    return hmac.new(key, msg.encode("utf-8"), hashlib.sha256).digest()


def parse_endpoint(endpoint, region="us-east-1"):
    """(scheme, host[:port], base path) from whatever the user typed."""
    endpoint = (endpoint or "").strip().replace("{region}", region)
    if not endpoint:
        endpoint = f"https://s3.{region}.amazonaws.com"
    if "://" not in endpoint:
        endpoint = "https://" + endpoint
    u = urllib.parse.urlsplit(endpoint)
    if not u.hostname:
        raise ValueError(f"Not a valid endpoint: {endpoint}")
    host = u.hostname if ":" not in u.hostname else f"[{u.hostname}]"
    port = u.port
    if port and not ((u.scheme == "https" and port == 443) or (u.scheme == "http" and port == 80)):
        host += f":{port}"
    return u.scheme, host, u.path.rstrip("/")


def strip_ns(data):
    return re.sub(rb'\sxmlns(:\w+)?="[^"]*"', b"", data, count=2)


def xml_root(data):
    return ET.fromstring(strip_ns(data))


def _text(node, tag, default=""):
    child = node.find(tag)
    return child.text or default if child is not None and child.text is not None else default


def iso(ts):
    """Second-precision UTC timestamp, the form the library index stores ('2026-09-29T11:37:57Z')."""
    return (ts or "")[:19] + "Z" if ts else ""


class Stream:
    """A response body being read piece by piece. close() hands the connection back when it was read to the end."""

    def __init__(self, client, conn, resp, key):
        self.client, self._conn, self.resp, self._key, self._done = client, conn, resp, key, False
        self.status, self.headers = resp.status, {k.lower(): v for k, v in resp.getheaders()}

    def read(self, n=1 << 18):
        data = self.resp.read(n)
        if not data:
            self._done = True
        return data

    def close(self):
        if self._conn is None:
            return
        conn, self._conn = self._conn, None
        if self._done and not self.resp.will_close:
            self.client._release(self._key, conn)
        else:
            conn.close()

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        self.close()


class S3Client:
    def __init__(self, conn: Connection, keep=16):
        self.conn = conn
        self.scheme, self.netloc, self.base = parse_endpoint(conn.endpoint, conn.region or "us-east-1")
        host = self.netloc.rsplit(":", 1)[0].strip("[]")
        mode = conn.addressing or "path"
        if mode == "auto":
            mode = "virtual" if re.search(r"(amazonaws\.com|aliyuncs\.com|myqcloud\.com)$", host) else "path"
        # Virtual-hosted style needs a DNS name; an IP address or a bare host can only be path style.
        self.virtual = mode == "virtual" and not re.fullmatch(r"[\d.]+|\[.*\]|localhost", host)
        self.region = conn.region or "us-east-1"
        self.keep, self._idle, self._lock = keep, {}, threading.Lock()
        self._ctx = None
        self._now = lambda: datetime.now(timezone.utc)  # a test seam: signatures depend on the clock
        if self.scheme == "https":
            self._ctx = ssl.create_default_context()
            if not conn.verify_tls:
                self._ctx.check_hostname, self._ctx.verify_mode = False, ssl.CERT_NONE

    # ---------------------------------------------------------------- addressing and signing
    def _target(self, bucket, key):
        """(host, canonical path) for a request."""
        host, path = self.netloc, self.base + "/"
        if bucket and self.virtual:
            host = f"{bucket}.{self.netloc}"
            path = self.base + "/" + (key_path(key) if key else "")
        elif bucket:
            path = f"{self.base}/{quote(bucket)}" + ("/" + key_path(key) if key is not None else "")
        return host, path

    def _canonical_query(self, query):
        return "&".join(f"{quote(str(k))}={quote(str(v))}" for k, v in sorted((query or {}).items(), key=lambda kv: (str(kv[0]), str(kv[1]))))

    def _signing_key(self, date):
        k = _hmac(("AWS4" + self.conn.secret_key).encode("utf-8"), date)
        for part in (self.region, "s3", "aws4_request"):
            k = _hmac(k, part)
        return k

    def _signed_headers(self, method, host, path, query, headers, payload_hash):
        now = self._now()
        amz_date, date = now.strftime("%Y%m%dT%H%M%SZ"), now.strftime("%Y%m%d")
        h = {k.lower(): str(v) for k, v in (headers or {}).items()}
        h.update({"host": host, "x-amz-date": amz_date, "x-amz-content-sha256": payload_hash})
        if self.conn.session_token:
            h["x-amz-security-token"] = self.conn.session_token
        names = sorted(h)
        canonical = "\n".join([
            method, path, self._canonical_query(query),
            "".join(f"{k}:{' '.join(h[k].split())}\n" for k in names), ";".join(names), payload_hash,
        ])
        scope = f"{date}/{self.region}/s3/aws4_request"
        to_sign = "\n".join(["AWS4-HMAC-SHA256", amz_date, scope, hashlib.sha256(canonical.encode("utf-8")).hexdigest()])
        sig = hmac.new(self._signing_key(date), to_sign.encode("utf-8"), hashlib.sha256).hexdigest()
        h["authorization"] = (f"AWS4-HMAC-SHA256 Credential={self.conn.access_key}/{scope}, "
                              f"SignedHeaders={';'.join(names)}, Signature={sig}")
        h.pop("host")  # http.client sets Host itself, to the same value
        return h

    def presign(self, method, bucket, key, expires=3600, query=None, response_headers=None):
        """A URL that grants `method` on one object for `expires` seconds, with no credentials needed."""
        if not 1 <= expires <= 7 * 86400:
            raise ValueError("A presigned URL can be valid for 1 second to 7 days.")
        host, path = self._target(bucket, key)
        now = self._now()
        amz_date, date = now.strftime("%Y%m%dT%H%M%SZ"), now.strftime("%Y%m%d")
        scope = f"{date}/{self.region}/s3/aws4_request"
        q = dict(query or {})
        for name, value in (response_headers or {}).items():  # e.g. {"response-content-disposition": "attachment"}
            q[name] = value
        q.update({"X-Amz-Algorithm": "AWS4-HMAC-SHA256", "X-Amz-Credential": f"{self.conn.access_key}/{scope}",
                  "X-Amz-Date": amz_date, "X-Amz-Expires": str(int(expires)), "X-Amz-SignedHeaders": "host"})
        if self.conn.session_token:
            q["X-Amz-Security-Token"] = self.conn.session_token
        canonical = "\n".join([method, path, self._canonical_query(q), f"host:{host}\n", "host", UNSIGNED])
        to_sign = "\n".join(["AWS4-HMAC-SHA256", amz_date, scope, hashlib.sha256(canonical.encode("utf-8")).hexdigest()])
        sig = hmac.new(self._signing_key(date), to_sign.encode("utf-8"), hashlib.sha256).hexdigest()
        return f"{self.scheme}://{host}{path}?{self._canonical_query(q)}&X-Amz-Signature={sig}"

    # ---------------------------------------------------------------- transport
    def _connect(self, host):
        cls = http.client.HTTPSConnection if self.scheme == "https" else http.client.HTTPConnection
        kw = {"context": self._ctx} if self.scheme == "https" else {}
        return cls(host, timeout=self.conn.timeout, **kw)

    def _acquire(self, host):
        with self._lock:
            idle = self._idle.get(host)
            conn = idle.pop() if idle else None
        return (conn, True) if conn else (self._connect(host), False)

    def _release(self, host, conn):
        with self._lock:
            idle = self._idle.setdefault(host, [])
            if len(idle) < self.keep:
                idle.append(conn)
                return
        conn.close()

    def close(self):
        with self._lock:
            conns = [c for lst in self._idle.values() for c in lst]
            self._idle.clear()
        for c in conns:
            c.close()

    def _once(self, method, bucket, key, query, headers, body, stream):
        host, path = self._target(bucket, key)
        payload = body if isinstance(body, (bytes, bytearray, memoryview)) else b""
        payload_hash = hashlib.sha256(payload).hexdigest() if payload else EMPTY_SHA
        hdrs = self._signed_headers(method, host, path, query, headers, payload_hash)
        if payload or method in ("PUT", "POST"):
            hdrs["content-length"] = str(len(payload))
        target = path + ("?" + self._canonical_query(query) if query else "")
        while True:
            conn, reused = self._acquire(host)
            try:
                conn.request(method, target, body=payload if payload else None, headers=hdrs)
                resp = conn.getresponse()
            except (http.client.HTTPException, OSError) as e:
                conn.close()
                if reused:
                    continue  # a kept-alive connection the server had already dropped: go again on a fresh one
                raise S3Error(0, "ConnectionError", f"Cannot reach {host}: {e}", retryable=True) from None
            break
        if resp.status >= 400 or not stream:
            try:
                data = resp.read() if method != "HEAD" else b""
            except (http.client.HTTPException, OSError) as e:
                conn.close()
                raise S3Error(0, "ConnectionError", f"Connection to {host} dropped: {e}", retryable=True) from None
            if resp.will_close:
                conn.close()
            else:
                self._release(host, conn)
            headers_out = {k.lower(): v for k, v in resp.getheaders()}
            if resp.status >= 400:
                raise self._error(resp.status, headers_out, data, method)
            return resp.status, headers_out, data
        return Stream(self, conn, resp, host)

    @staticmethod
    def _error(status, headers, data, method):
        code, message, request_id, extra = "", "", headers.get("x-amz-request-id"), {}
        if data and data.lstrip()[:1] == b"<":
            try:
                root = xml_root(data)
                code, message = _text(root, "Code"), _text(root, "Message")
                request_id = _text(root, "RequestId") or request_id
                extra = {c.tag: c.text for c in root if c.tag in ("Region", "Endpoint", "BucketName", "Key")}
            except ET.ParseError:
                pass
        if not code:
            code = {301: "PermanentRedirect", 400: "BadRequest", 403: "AccessDenied", 404: "NotFound", 409: "Conflict",
                    412: "PreconditionFailed", 416: "InvalidRange", 503: "SlowDown"}.get(status, f"HTTP{status}")
            if method == "HEAD" and not message:
                message = {403: "Access denied", 404: "Not found"}.get(status, "")
        err = S3Error(status, code, message or code, request_id, retryable=status in RETRY_STATUS or code in ("SlowDown", "RequestTimeout", "InternalError"))
        err.extra = extra
        return err

    def request(self, method, bucket=None, key=None, query=None, headers=None, body=b"", stream=False, tries=4):
        """Signed request with retries on network trouble and 5xx. Returns (status, headers, body), or a Stream."""
        last = None
        for attempt in range(tries):
            try:
                return self._once(method, bucket, key, query, headers, body, stream)
            except S3Error as e:
                # A store in another region names it in the error: take its word for it and sign again.
                hint = getattr(e, "extra", {}).get("Region")
                if hint and hint != self.region and e.code in ("AuthorizationHeaderMalformed", "IllegalLocationConstraintException", "PermanentRedirect"):
                    self.region = hint
                    continue
                if not e.retryable or attempt == tries - 1:
                    raise
                last = e
                time.sleep(0.4 * 2 ** attempt)
        raise last  # unreachable, but keeps the contract obvious

    # ---------------------------------------------------------------- buckets
    def list_buckets(self):
        _, _, data = self.request("GET")
        root = xml_root(data)
        return [{"name": _text(b, "Name"), "created": iso(_text(b, "CreationDate"))} for b in root.iter("Bucket")]

    def create_bucket(self, bucket, region=None):
        region = region or self.region
        body = b""
        if region not in ("us-east-1", "auto", ""):
            body = f'<CreateBucketConfiguration><LocationConstraint>{region}</LocationConstraint></CreateBucketConfiguration>'.encode()
        self.request("PUT", bucket, None, body=body)

    def delete_bucket(self, bucket):
        self.request("DELETE", bucket, None)

    def head_bucket(self, bucket):
        _, h, _ = self.request("HEAD", bucket, None)
        return {"region": h.get("x-amz-bucket-region", self.region)}

    # ---------------------------------------------------------------- listing
    def list_objects(self, bucket, prefix="", delimiter="/", token=None, max_keys=1000):
        """One page (ListObjectsV2). Returns {"prefixes": [...], "objects": [...], "next": token or None}."""
        q = {"list-type": "2", "max-keys": str(max_keys), "encoding-type": "url"}
        if prefix:
            q["prefix"] = prefix
        if delimiter:
            q["delimiter"] = delimiter
        if token:
            q["continuation-token"] = token
        _, _, data = self.request("GET", bucket, None, query=q)
        root = xml_root(data)
        dec = urllib.parse.unquote if _text(root, "EncodingType") == "url" else (lambda s: s)
        objects = [{"key": dec(_text(c, "Key")), "size": int(_text(c, "Size", "0")), "mtime": iso(_text(c, "LastModified")),
                    "etag": _text(c, "ETag").strip('"'), "storage_class": _text(c, "StorageClass", "STANDARD")}
                   for c in root.findall("Contents")]
        prefixes = [dec(_text(p, "Prefix")) for p in root.findall("CommonPrefixes")]
        more = _text(root, "IsTruncated") == "true"
        return {"prefixes": prefixes, "objects": objects, "next": _text(root, "NextContinuationToken") or None if more else None}

    def iter_objects(self, bucket, prefix="", delimiter=""):
        """Every object under a prefix, page after page."""
        token = None
        while True:
            page = self.list_objects(bucket, prefix, delimiter, token)
            yield from page["objects"]
            token = page["next"]
            if not token:
                return

    def list_uploads(self, bucket, prefix=""):
        """Multipart uploads that were started and never finished (they still take up space)."""
        out, marker, upload_marker = [], None, None
        while True:
            q = {"uploads": "", "encoding-type": "url"}
            if prefix:
                q["prefix"] = prefix
            if marker:
                q["key-marker"], q["upload-id-marker"] = marker, upload_marker
            _, _, data = self.request("GET", bucket, None, query=q)
            root = xml_root(data)
            for u in root.findall("Upload"):
                out.append({"key": urllib.parse.unquote(_text(u, "Key")), "upload_id": _text(u, "UploadId"), "started": iso(_text(u, "Initiated"))})
            if _text(root, "IsTruncated") != "true":
                return out
            marker, upload_marker = urllib.parse.unquote(_text(root, "NextKeyMarker")), _text(root, "NextUploadIdMarker")

    # ---------------------------------------------------------------- objects
    @staticmethod
    def _object_info(h):
        meta = {k[11:]: urllib.parse.unquote(v) for k, v in h.items() if k.startswith("x-amz-meta-")}
        size = int(h.get("content-length") or 0)
        if "content-range" in h and "/" in h["content-range"]:
            size = int(h["content-range"].rsplit("/", 1)[1] or size)
        mtime = ""
        if h.get("last-modified"):
            try:
                mtime = datetime.strptime(h["last-modified"], "%a, %d %b %Y %H:%M:%S GMT").strftime("%Y-%m-%dT%H:%M:%SZ")
            except ValueError:
                mtime = h["last-modified"]
        return {"size": size, "mtime": mtime, "etag": h.get("etag", "").strip('"'), "content_type": h.get("content-type", ""),
                "cache_control": h.get("cache-control", ""), "content_disposition": h.get("content-disposition", ""),
                "content_encoding": h.get("content-encoding", ""), "storage_class": h.get("x-amz-storage-class", "STANDARD"),
                "version_id": h.get("x-amz-version-id", ""), "metadata": meta}

    def head_object(self, bucket, key):
        _, h, _ = self.request("HEAD", bucket, key)
        return self._object_info(h)

    def get_object(self, bucket, key, byte_range=None, stream=False, headers=None):
        """Whole object or a byte range as bytes, or with stream=True a Stream (check .status: 200 or 206)."""
        hdrs = dict(headers or {})
        if byte_range:
            hdrs["Range"] = byte_range if isinstance(byte_range, str) else f"bytes={byte_range[0]}-{byte_range[1]}"
        if stream:
            return self.request("GET", bucket, key, headers=hdrs, stream=True)
        return self.request("GET", bucket, key, headers=hdrs)[2]

    @staticmethod
    def _meta_headers(content_type=None, metadata=None, extra=None):
        h = dict(extra or {})
        if content_type:
            h["Content-Type"] = content_type
        for k, v in (metadata or {}).items():
            v = str(v)
            try:
                v.encode("ascii")
            except UnicodeEncodeError:
                v = quote(v)  # headers are ASCII only: stored percent-encoded, decoded again on the way out
            h[f"x-amz-meta-{k.lower()}"] = v
        return h

    def put_object(self, bucket, key, data, content_type=None, metadata=None, headers=None):
        _, h, _ = self.request("PUT", bucket, key, headers=self._meta_headers(content_type, metadata, headers), body=data)
        return h.get("etag", "").strip('"')

    def delete_object(self, bucket, key):
        self.request("DELETE", bucket, key)

    def delete_objects(self, bucket, keys):
        """Delete up to 1000 keys in one call. Returns (deleted keys, [{"key", "code", "message"}])."""
        deleted, failed = [], []
        for i in range(0, len(keys), 1000):
            batch = keys[i:i + 1000]
            body = ("<Delete><Quiet>false</Quiet>" + "".join(f"<Object><Key>{_xml_escape(k)}</Key></Object>" for k in batch)
                    + "</Delete>").encode("utf-8")
            md5 = base64.b64encode(hashlib.md5(body).digest()).decode()  # S3 insists on a checksum here
            _, _, data = self.request("POST", bucket, None, query={"delete": ""}, body=body,
                                      headers={"Content-MD5": md5, "Content-Type": "application/xml"})
            root = xml_root(data)
            deleted += [_text(d, "Key") for d in root.findall("Deleted")]
            failed += [{"key": _text(e, "Key"), "code": _text(e, "Code"), "message": _text(e, "Message")} for e in root.findall("Error")]
        return deleted, failed

    def copy_object(self, src_bucket, src_key, bucket, key, size=None, content_type=None, metadata=None, replace=False, headers=None):
        """Server-side copy. With replace=True the new content type / metadata replace the source's (also in place)."""
        hdrs = self._meta_headers(content_type, metadata, headers)
        hdrs["x-amz-copy-source"] = f"/{quote(src_bucket)}/{key_path(src_key)}"
        if replace:
            hdrs["x-amz-metadata-directive"] = "REPLACE"
        if size is not None and size > 4 * 1024 ** 3:  # beyond what a single copy request allows
            return self._copy_multipart(src_bucket, src_key, bucket, key, size, hdrs)
        _, _, data = self.request("PUT", bucket, key, headers=hdrs)
        root = xml_root(data) if data else None
        if root is not None and root.tag == "Error":  # S3 can answer 200 and then fail the copy in the body
            raise S3Error(500, _text(root, "Code"), _text(root, "Message"), retryable=False)

    def _copy_multipart(self, src_bucket, src_key, bucket, key, size, hdrs):
        hdrs = {k: v for k, v in hdrs.items() if k != "x-amz-copy-source"}
        upload = self.create_multipart(bucket, key, headers=hdrs)
        try:
            parts, step = [], 512 * 1024 ** 2
            for n, start in enumerate(range(0, size, step), 1):
                end = min(size, start + step) - 1
                _, _, data = self.request("PUT", bucket, key, query={"partNumber": str(n), "uploadId": upload}, headers={
                    "x-amz-copy-source": f"/{quote(src_bucket)}/{key_path(src_key)}", "x-amz-copy-source-range": f"bytes={start}-{end}"})
                parts.append((n, _text(xml_root(data), "ETag")))
            self.complete_multipart(bucket, key, upload, parts)
        except BaseException:
            self.abort_multipart(bucket, key, upload)
            raise

    # ---------------------------------------------------------------- multipart upload
    def create_multipart(self, bucket, key, content_type=None, metadata=None, headers=None):
        _, _, data = self.request("POST", bucket, key, query={"uploads": ""}, headers=self._meta_headers(content_type, metadata, headers))
        return _text(xml_root(data), "UploadId")

    def upload_part(self, bucket, key, upload_id, number, data):
        _, h, _ = self.request("PUT", bucket, key, query={"partNumber": str(number), "uploadId": upload_id}, body=data)
        return h.get("etag", "")

    def complete_multipart(self, bucket, key, upload_id, parts):
        body = ("<CompleteMultipartUpload>" + "".join(f"<Part><PartNumber>{n}</PartNumber><ETag>{_xml_escape(e)}</ETag></Part>"
                                                     for n, e in sorted(parts)) + "</CompleteMultipartUpload>").encode()
        _, _, data = self.request("POST", bucket, key, query={"uploadId": upload_id}, body=body, headers={"Content-Type": "application/xml"})
        root = xml_root(data) if data else None
        if root is not None and root.tag == "Error":
            raise S3Error(500, _text(root, "Code"), _text(root, "Message"))

    def abort_multipart(self, bucket, key, upload_id):
        try:
            self.request("DELETE", bucket, key, query={"uploadId": upload_id})
        except S3Error:
            pass


def _xml_escape(s):
    return s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


def plan_parts(size, part_size):
    """(part size, count) so the object fits in 10,000 parts, parts being at least 5 MiB."""
    part = max(MIN_PART, part_size)
    while size > part * MAX_PARTS:
        part *= 2
    return part, max(1, -(-size // part))
