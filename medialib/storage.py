"""Object-store operations behind the Storage view: browse, search, upload, copy, move, delete, properties.

Pure logic on top of S3Client; the HTTP layer (server.py) only translates requests into these calls.
"""
import concurrent.futures as cf
import mimetypes

from .s3 import S3Error, plan_parts
from .tasks import Tasks

PART = 16 * 1024 * 1024       # upload part size: big enough to be efficient, small enough to hold three in memory
CROSS_CHUNK = 16 * 1024 * 1024
MAX_SCAN = 50_000             # keys a name search looks through before giving up


def guess_type(name):
    return mimetypes.guess_type(name)[0] or "application/octet-stream"


def check_key(key):
    if not key or not key.strip("/"):
        raise ValueError("The object name is empty.")
    if len(key.encode("utf-8")) > 1024:
        raise ValueError("Object names are limited to 1024 bytes.")
    return key


def leaf(key):
    return key.rstrip("/").rpartition("/")[2]


class Storage:
    def __init__(self, clients, tasks: Tasks):
        self.clients, self.tasks = clients, tasks

    # ---------------------------------------------------------------- browsing
    def buckets(self, conn):
        return sorted(self.clients.get(conn).list_buckets(), key=lambda b: b["name"])

    def list(self, conn, bucket, prefix="", token=None, limit=500, delimiter="/"):
        page = self.clients.get(conn).list_objects(bucket, prefix, delimiter, token, max(1, min(limit, 1000)))
        # Zero-byte "folder marker" objects (key ends with "/") would otherwise show up as an empty file named "".
        page["objects"] = [o for o in page["objects"] if o["key"] != prefix or not o["key"].endswith("/")]
        return page

    def search(self, conn, bucket, prefix, text, limit=300):
        """Look through everything under a prefix for names containing `text` (case-insensitive)."""
        client, needle, found, scanned = self.clients.get(conn), text.lower(), [], 0
        truncated = False
        for o in client.iter_objects(bucket, prefix):
            scanned += 1
            if needle in o["key"][len(prefix):].lower() and not o["key"].endswith("/"):
                found.append(o)
                if len(found) >= limit:
                    truncated = True
                    break
            if scanned >= MAX_SCAN:
                truncated = True
                break
        return {"objects": found, "scanned": scanned, "truncated": truncated}

    def head(self, conn, bucket, key):
        info = self.clients.get(conn).head_object(bucket, key)
        if info["content_type"] in ("", "binary/octet-stream", "application/octet-stream"):
            info["guessed_type"] = guess_type(key)
        return info

    def make_folder(self, conn, bucket, prefix):
        prefix = prefix.strip("/") + "/"
        if prefix == "/":
            raise ValueError("Enter a folder name.")
        self.clients.get(conn).put_object(bucket, prefix, b"", content_type="application/x-directory")
        return prefix

    # ---------------------------------------------------------------- properties
    def set_properties(self, conn, bucket, key, content_type=None, metadata=None, cache_control=None, content_disposition=None):
        """Rewrite an object's headers and metadata in place (S3 can only do this by copying it onto itself)."""
        client = self.clients.get(conn)
        cur = client.head_object(bucket, key)
        extra = {}
        cc = cur["cache_control"] if cache_control is None else cache_control
        cd = cur["content_disposition"] if content_disposition is None else content_disposition
        if cc:
            extra["Cache-Control"] = cc
        if cd:
            extra["Content-Disposition"] = cd
        if cur["content_encoding"]:
            extra["Content-Encoding"] = cur["content_encoding"]
        client.copy_object(bucket, key, bucket, key, size=cur["size"], replace=True,
                           content_type=content_type or cur["content_type"] or guess_type(key),
                           metadata=cur["metadata"] if metadata is None else metadata, headers=extra)
        return client.head_object(bucket, key)

    # ---------------------------------------------------------------- upload
    def upload(self, conn, bucket, key, length, read, content_type=None, overwrite=True, on_bytes=None):
        """Store `length` bytes produced by read(n). Large bodies go up as a multipart upload, three parts at a time."""
        client = self.clients.get(conn)
        check_key(key)
        if not overwrite:
            try:
                client.head_object(bucket, key)
                raise ValueError(f"“{key}” already exists.")
            except S3Error as e:
                if e.status != 404:
                    raise
        ctype = content_type or guess_type(key)
        if length <= PART:
            data = _read_exact(read, length)
            etag = client.put_object(bucket, key, data, content_type=ctype)
            if on_bytes:
                on_bytes(length)
            return {"key": key, "size": length, "etag": etag}
        part, _ = plan_parts(length, PART)
        upload_id = client.create_multipart(bucket, key, content_type=ctype)
        parts, pending, sent = [], set(), 0
        pool = cf.ThreadPoolExecutor(3)
        try:
            number = 0
            while sent < length:
                chunk = _read_exact(read, min(part, length - sent))
                sent += len(chunk)
                number += 1
                pending.add(pool.submit(lambda n=number, c=chunk: (n, client.upload_part(bucket, key, upload_id, n, c), len(c))))
                while len(pending) >= 3:  # backpressure: the browser is not read faster than the store takes parts
                    done, pending = cf.wait(pending, return_when=cf.FIRST_COMPLETED)
                    for f in done:
                        n, etag, size = f.result()
                        parts.append((n, etag))
                        if on_bytes:
                            on_bytes(size)
            for f in cf.as_completed(pending):
                n, etag, size = f.result()
                parts.append((n, etag))
                if on_bytes:
                    on_bytes(size)
            client.complete_multipart(bucket, key, upload_id, parts)
        except BaseException:
            pool.shutdown(wait=False, cancel_futures=True)
            client.abort_multipart(bucket, key, upload_id)
            raise
        pool.shutdown()
        return {"key": key, "size": length, "etag": ""}

    # ---------------------------------------------------------------- tasks
    def delete(self, conn, bucket, keys, prefixes):
        client = self.clients.get(conn)
        what = f"{len(keys)} object{'s' if len(keys) != 1 else ''}" + (f" and {len(prefixes)} folder{'s' if len(prefixes) != 1 else ''}" if prefixes else "")

        def work(task):
            task.total = len(keys)
            batches = [keys[i:i + 1000] for i in range(0, len(keys), 1000)]
            for batch in batches:
                task.check()
                task.line = f"Deleting {len(batch)} objects"
                self._flush(client, bucket, batch, task)
            for prefix in prefixes:
                task.line = f"Deleting everything under {prefix}"
                batch = []
                for o in client.iter_objects(bucket, prefix):
                    task.check()
                    batch.append(o["key"])
                    task.total += 1
                    if len(batch) >= 1000:
                        self._flush(client, bucket, batch, task)
                        batch = []
                if batch:
                    self._flush(client, bucket, batch, task)
                try:
                    client.delete_object(bucket, prefix)  # the folder's own marker, if there is one
                except S3Error:
                    pass
            task.line = f"Deleted {task.done} of {task.total}"

        return self.tasks.start("delete", f"Delete {what} from {bucket}", work)

    @staticmethod
    def _flush(client, bucket, batch, task):
        deleted, failed = client.delete_objects(bucket, batch)
        task.done += len(deleted)
        for f in failed:
            task.fail(f"{f['key']}: {f['message'] or f['code']}")

    def measure(self, conn, bucket, prefix):
        client = self.clients.get(conn)

        def work(task):
            n = size = 0
            for o in client.iter_objects(bucket, prefix):
                task.check()
                n += 1
                size += o["size"]
                if n % 500 == 0:
                    task.done, task.bytes = n, size
                    task.line = f"{n} objects so far"
            task.done, task.bytes, task.total = n, size, n
            task.result = {"objects": n, "bytes": size}
            task.line = f"{n} objects"

        return self.tasks.start("size", f"Size of {bucket}/{prefix}", work)

    def transfer(self, conn, bucket, items, to_conn=None, to_bucket=None, move=False, skip_existing=True):
        """Copy or move objects/folders. items: [{"from": key-or-prefix/, "to": key-or-prefix/}]. A trailing "/" means a folder.

        Within one connection the store copies server side. Between connections the bytes pass through this machine."""
        src, dst_conn, dst_bucket = self.clients.get(conn), to_conn or conn, to_bucket or bucket
        dst = self.clients.get(dst_conn)
        same = src is dst
        verb = "Move" if move else "Copy"

        def work(task):
            pairs = []
            for it in items:
                task.check()
                a, b = it["from"], it["to"]
                if a.endswith("/"):
                    if same and bucket == dst_bucket and (b == a or b.startswith(a)):
                        raise ValueError(f"Can't {verb.lower()} “{a}” into itself.")
                    task.line = f"Listing {a}"
                    for o in src.iter_objects(bucket, a):
                        pairs.append((o["key"], b + o["key"][len(a):], o["size"]))
                else:
                    pairs.append((a, b, None))
            task.total = len(pairs)
            done_keys = []
            for k, (a, b, size) in enumerate(pairs):
                task.check()
                task.line = leaf(a) or a
                if same and bucket == dst_bucket and a == b:
                    task.done += 1
                    continue
                try:
                    if skip_existing:
                        try:
                            dst.head_object(dst_bucket, b)
                            task.fail(f"{b}: already exists, skipped")
                            continue
                        except S3Error as e:
                            if e.status != 404:
                                raise
                    task.bytes += self._copy_one(src, bucket, a, dst, dst_bucket, b, size, task)
                    task.done += 1
                    done_keys.append(a)
                except S3Error as e:
                    task.fail(f"{a}: {e}")
                if move and len(done_keys) >= 500:
                    src.delete_objects(bucket, done_keys)
                    done_keys = []
            if move and done_keys:
                src.delete_objects(bucket, done_keys)
            task.line = f"{verb} finished: {task.done} of {task.total}"

        title = f"{verb} {len(items)} item{'s' if len(items) != 1 else ''} to {dst_bucket}"
        return self.tasks.start(verb.lower(), title, work)

    @staticmethod
    def _copy_one(src, sb, sk, dst, db, dk, size, task):
        if src is dst:
            if size is None:
                size = src.head_object(sb, sk)["size"]
            src.copy_object(sb, sk, db, dk, size=size)
            return size
        info = src.head_object(sb, sk)
        size, ctype, meta = info["size"], info["content_type"] or guess_type(sk), info["metadata"]
        if size <= CROSS_CHUNK:
            dst.put_object(db, dk, src.get_object(sb, sk) if size else b"", content_type=ctype, metadata=meta)
            return size
        upload_id = dst.create_multipart(db, dk, content_type=ctype, metadata=meta)
        try:
            parts = []
            for n, start in enumerate(range(0, size, CROSS_CHUNK), 1):
                task.check()
                data = src.get_object(sb, sk, (start, min(size, start + CROSS_CHUNK) - 1))
                parts.append((n, dst.upload_part(db, dk, upload_id, n, data)))
            dst.complete_multipart(db, dk, upload_id, parts)
        except BaseException:
            dst.abort_multipart(db, dk, upload_id)
            raise
        return size

    # ---------------------------------------------------------------- housekeeping
    def incomplete_uploads(self, conn, bucket, prefix=""):
        return self.clients.get(conn).list_uploads(bucket, prefix)

    def abort_uploads(self, conn, bucket, items):
        client = self.clients.get(conn)
        for it in items:
            client.abort_multipart(bucket, it["key"], it["upload_id"])
        return len(items)


def _read_exact(read, n):
    """n bytes from read(), which may return fewer at a time."""
    buf = bytearray()
    while len(buf) < n:
        chunk = read(min(1 << 20, n - len(buf)))
        if not chunk:
            raise ConnectionError("The upload ended before all bytes arrived.")
        buf += chunk
    return bytes(buf)
