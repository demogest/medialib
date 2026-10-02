"""End-to-end: the medialib binary serving the API, talking to a local mock S3 (moto).

Run from the repository root:  python -m unittest discover -s tests/e2e -t .
Needs `pip install -r requirements-dev.txt` (moto, boto3), Go to build the binary (or MEDIALIB_BIN), and ffmpeg for
the library test. Skipped when moto is missing.
"""
import hashlib
import json
import os
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import unittest
import urllib.error
import urllib.parse
import urllib.request

try:
    import moto  # noqa: F401
except ImportError:
    moto = None

from tests.e2e.common import ROOT, Store, binary, free_port, wait_port  # noqa: E402

AK, SK = "testkey", "testsecret"


@unittest.skipUnless(moto, "moto is not installed")
class Api(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.home = tempfile.mkdtemp(prefix="medialib-test-")
        cls.s3_port, cls.port = free_port(), free_port()
        cls.moto = subprocess.Popen([sys.executable, "-m", "moto.server", "-p", str(cls.s3_port)], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        env = {**os.environ, "MEDIALIB_HOME": cls.home}
        cls.server = subprocess.Popen([binary(), "serve", "--no-browser", "--port", str(cls.port)], cwd=ROOT, env=env,
                                      stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        wait_port(cls.s3_port)
        wait_port(cls.port)
        cls.conn = cls.post("/api/connections", {"name": "Mock", "provider": "minio", "endpoint": f"http://127.0.0.1:{cls.s3_port}",
                                                 "access_key": AK, "secret_key": SK})["id"]

    @classmethod
    def tearDownClass(cls):
        for p in (cls.server, cls.moto):
            p.terminate()
            p.wait(10)
        shutil.rmtree(cls.home, ignore_errors=True)

    # ---- helpers
    @classmethod
    def call(cls, method, path, body=None, raw=None, headers=None, check=True):
        h = {"X-Medialib": "1", **(headers or {})}
        data = raw
        if body is not None:
            data, h["Content-Type"] = json.dumps(body).encode(), "application/json"
        req = urllib.request.Request(f"http://127.0.0.1:{cls.port}{path}", data=data, method=method, headers=h)
        try:
            with urllib.request.urlopen(req, timeout=60) as r:
                payload = r.read()
                return r.status, (json.loads(payload) if r.headers.get_content_type() == "application/json" else payload), r.headers
        except urllib.error.HTTPError as e:
            payload = e.read()
            if check:
                raise AssertionError(f"{method} {path} -> {e.code} {payload[:300]!r}") from None
            return e.code, (json.loads(payload) if payload[:1] == b"{" else payload), e.headers

    @classmethod
    def get(cls, path, **kw):
        return cls.call("GET", path, **kw)[1]

    @classmethod
    def post(cls, path, body=None, **kw):
        return cls.call("POST", path, body or {}, **kw)[1]

    def b(self, bucket, action, query=""):
        return f"/api/s3/{self.conn}/b/{bucket}/{action}{('?' + query) if query else ''}"

    def bucket(self, name):
        self.post(f"/api/s3/{self.conn}/buckets", {"name": name})
        return name

    def put(self, bucket, key, data, ctype="application/octet-stream"):
        q = urllib.parse.urlencode({"key": key})
        return self.call("PUT", self.b(bucket, "object", q), raw=data, headers={"Content-Type": ctype})[1]

    def finish(self, result):
        """A task answer: wait for it if it did not finish within the request."""
        task = result["task"]
        for _ in range(100):
            if task["state"] != "running":
                return task
            time.sleep(0.1)
            task = next(t for t in self.get("/api/tasks")["tasks"] if t["id"] == task["id"])
        self.fail("task did not finish")

    # ---- connections
    def test_connection_never_returns_its_secret(self):
        text = json.dumps(self.get("/api/connections"))
        self.assertNotIn(SK, text)
        conn = next(c for c in self.get("/api/connections")["connections"] if c["id"] == self.conn)
        self.assertTrue(conn["has_secret"])
        self.assertEqual(conn["secret_source"], "config")

    def test_test_connection(self):
        r = self.post(f"/api/connections/{self.conn}/test")
        self.assertTrue(r["ok"], r)
        self.assertIsInstance(r["buckets"], list)
        # A draft with a blank secret reuses the stored one of the connection being edited.
        r = self.post("/api/connections/test", {"id": self.conn, "provider": "minio", "endpoint": f"http://127.0.0.1:{self.s3_port}", "access_key": AK})
        self.assertTrue(r["ok"], r)

    def test_unreachable_endpoint_reports_instead_of_crashing(self):
        r = self.post("/api/connections/test", {"provider": "other", "endpoint": f"http://127.0.0.1:{free_port()}", "access_key": "a", "secret_key": "b"})
        self.assertFalse(r["ok"])
        self.assertIn("Cannot reach", r["message"])

    def test_validation(self):
        status, body, _ = self.call("POST", "/api/connections", {"provider": "other", "endpoint": "", "access_key": "x", "secret_key": "y"}, check=False)
        self.assertEqual(status, 400)
        self.assertIn("endpoint", body["error"])

    # ---- security
    def test_changes_need_the_custom_header_and_a_local_host(self):
        req = urllib.request.Request(f"http://127.0.0.1:{self.port}/api/connections", data=b"{}", method="POST", headers={"Content-Type": "application/json"})
        with self.assertRaises(urllib.error.HTTPError) as cm:
            urllib.request.urlopen(req)
        self.assertEqual(cm.exception.code, 403)
        req = urllib.request.Request(f"http://127.0.0.1:{self.port}/api/libraries", headers={"Host": "evil.example"})
        with self.assertRaises(urllib.error.HTTPError) as cm:
            urllib.request.urlopen(req)
        self.assertEqual(cm.exception.code, 403)
        req = urllib.request.Request(f"http://127.0.0.1:{self.port}/api/libraries", headers={"Sec-Fetch-Site": "cross-site"})
        with self.assertRaises(urllib.error.HTTPError):
            urllib.request.urlopen(req)

    def test_static_path_traversal_is_refused(self):
        for p in ("/static/../config.json", "/static/..%2Fconfig.json", "/static/js/../../config.json"):
            status, _, _ = self.call("GET", p, check=False)
            self.assertEqual(status, 404, p)

    # ---- buckets and objects
    def test_buckets(self):
        name = self.bucket("bucket-a")
        self.assertIn(name, [b["name"] for b in self.get(f"/api/s3/{self.conn}/buckets")["buckets"]])
        self.call("DELETE", f"/api/s3/{self.conn}/buckets/{name}")
        self.assertNotIn(name, [b["name"] for b in self.get(f"/api/s3/{self.conn}/buckets")["buckets"]])
        status, body, _ = self.call("POST", f"/api/s3/{self.conn}/buckets", {"name": "BAD_NAME"}, check=False)
        self.assertEqual(status, 400)

    def test_upload_download_roundtrip_small_and_multipart(self):
        bkt = self.bucket("roundtrip")
        small = os.urandom(1000)
        self.put(bkt, "dir/small file é.bin", small)
        got = self.call("GET", f"/s3/{self.conn}/{bkt}/" + urllib.parse.quote("dir/small file é.bin"))[1]
        self.assertEqual(got, small)
        big = os.urandom(40 * 1024 * 1024 + 123)  # three parts
        self.put(bkt, "big.bin", big)
        status, data, headers = self.call("GET", f"/s3/{self.conn}/{bkt}/big.bin")
        self.assertEqual(hashlib.sha256(data).digest(), hashlib.sha256(big).digest())
        self.assertEqual(headers["Accept-Ranges"], "bytes")
        status, part, headers = self.call("GET", f"/s3/{self.conn}/{bkt}/big.bin", headers={"Range": "bytes=1000-1999"})
        self.assertEqual((status, part), (206, big[1000:2000]))
        self.assertTrue(headers["Content-Range"].startswith("bytes 1000-1999/"))
        self.assertEqual(self.get(self.b(bkt, "uploads"))["uploads"], [])  # nothing left half uploaded

    def test_upload_without_overwrite_refuses_existing_keys(self):
        bkt = self.bucket("overwrite")
        self.put(bkt, "a.txt", b"one", "text/plain")
        status, body, _ = self.call("PUT", self.b(bkt, "object", "key=a.txt&overwrite=0"), raw=b"two", check=False)
        self.assertEqual(status, 400)
        self.assertEqual(self.call("GET", f"/s3/{self.conn}/{bkt}/a.txt")[1], b"one")

    def test_listing_folders_pagination_and_search(self):
        bkt = self.bucket("listing")
        client = self.s3(bkt)
        for i in range(1050):
            client.put_object(bkt, f"many/{i:04d}.txt", b"x")
        client.put_object(bkt, "top.txt", b"x")
        client.put_object(bkt, "docs/readme.md", b"x")
        root = self.get(self.b(bkt, "list", "prefix=&delimiter=/"))
        self.assertEqual(sorted(root["prefixes"]), ["docs/", "many/"])
        self.assertEqual([o["key"] for o in root["objects"]], ["top.txt"])
        seen, token = [], None
        while True:
            page = self.get(self.b(bkt, "list", "prefix=many/&limit=400" + (f"&token={urllib.parse.quote(token)}" if token else "")))
            seen += [o["key"] for o in page["objects"]]
            token = page["next"]
            if not token:
                break
        self.assertEqual(len(seen), 1050)
        self.assertEqual(len(set(seen)), 1050)
        found = self.get(self.b(bkt, "search", "q=README"))
        self.assertEqual([o["key"] for o in found["objects"]], ["docs/readme.md"])

    def s3(self, bucket):
        return Store(f"http://127.0.0.1:{self.s3_port}", AK, SK)

    def test_folder_head_properties_and_presign(self):
        bkt = self.bucket("props")
        self.assertEqual(self.post(self.b(bkt, "folder"), {"prefix": "new folder/sub"})["prefix"], "new folder/sub/")
        self.assertIn("new folder/", self.get(self.b(bkt, "list", "prefix="))["prefixes"])
        self.assertEqual(self.get(self.b(bkt, "list", "prefix=new%20folder/sub/"))["objects"], [])  # the marker is hidden
        self.put(bkt, "movie.mp4", b"0123456789", "video/mp4")
        head = self.get(self.b(bkt, "head", "key=movie.mp4"))
        self.assertEqual((head["size"], head["content_type"]), (10, "video/mp4"))
        out = self.post(self.b(bkt, "properties"), {"key": "movie.mp4", "content_type": "video/x-test", "metadata": {"title": "Café"}, "cache_control": "max-age=60"})
        self.assertEqual((out["content_type"], out["metadata"], out["cache_control"]), ("video/x-test", {"title": "Café"}, "max-age=60"))
        self.assertEqual(self.get(self.b(bkt, "head", "key=movie.mp4"))["size"], 10)
        url = self.get(self.b(bkt, "presign", "key=movie.mp4&expires=600&download=1"))["url"]
        self.assertIn("X-Amz-Signature=", url)
        with urllib.request.urlopen(url) as r:
            self.assertEqual(r.read(), b"0123456789")

    def test_delete_keys_and_folders(self):
        bkt = self.bucket("deleting")
        c = self.s3(bkt)
        for k in ("a/1", "a/2", "a/deep/3", "b/4", "keep"):
            c.put_object(bkt, k, b"x")
        task = self.finish(self.post(self.b(bkt, "delete"), {"keys": ["b/4"], "prefixes": ["a/"]}))
        self.assertEqual((task["state"], task["done"], task["error_count"]), ("done", 4, 0))
        self.assertEqual([o["key"] for o in c.iter_objects(bkt)], ["keep"])

    def test_copy_move_between_prefixes_buckets_and_connections(self):
        src, dst = self.bucket("transfer-src"), self.bucket("transfer-dst")
        c = self.s3(src)
        c.put_object(src, "dir/a.txt", b"A", content_type="text/plain")
        c.put_object(src, "dir/sub/b.txt", b"B")
        big = os.urandom(20 * 1024 * 1024)
        self.put(src, "dir/big.bin", big)
        # copy a folder within the bucket
        t = self.finish(self.post(self.b(src, "transfer"), {"items": [{"from": "dir/", "to": "copy/"}]}))
        self.assertEqual((t["state"], t["done"]), ("done", 3), t)
        self.assertEqual(sorted(o["key"] for o in c.iter_objects(src, "copy/")), ["copy/a.txt", "copy/big.bin", "copy/sub/b.txt"])
        # an existing target is skipped, not overwritten
        c.put_object(src, "copy/a.txt", b"changed")
        t = self.finish(self.post(self.b(src, "transfer"), {"items": [{"from": "dir/a.txt", "to": "copy/a.txt"}]}))
        self.assertEqual(t["error_count"], 1)
        self.assertEqual(c.get_object(src, "copy/a.txt"), b"changed")
        # rename (move) a single file
        self.finish(self.post(self.b(src, "transfer"), {"items": [{"from": "dir/a.txt", "to": "renamed.txt"}], "move": True}))
        self.assertEqual(c.get_object(src, "renamed.txt"), b"A")
        self.assertNotIn("dir/a.txt", [o["key"] for o in c.iter_objects(src, "dir/")])
        # into another bucket
        self.finish(self.post(self.b(src, "transfer"), {"items": [{"from": "dir/sub/", "to": "moved/"}], "to_bucket": dst, "move": True}))
        self.assertEqual([o["key"] for o in c.iter_objects(dst)], ["moved/b.txt"])
        # into another connection: the bytes travel through medialib (multipart for the big one)
        other = self.post("/api/connections", {"name": "Mock 2", "provider": "minio", "endpoint": f"http://127.0.0.1:{self.s3_port}", "access_key": AK, "secret_key": SK})["id"]
        t = self.finish(self.post(self.b(src, "transfer"), {"items": [{"from": "dir/big.bin", "to": "x/big.bin"}], "to_conn": other, "to_bucket": dst}))
        self.assertEqual((t["state"], t["error_count"]), ("done", 0), t)
        self.assertEqual(hashlib.sha256(c.get_object(dst, "x/big.bin")).digest(), hashlib.sha256(big).digest())

    def test_cannot_copy_a_folder_into_itself(self):
        bkt = self.bucket("selfcopy")
        self.s3(bkt).put_object(bkt, "a/x", b"1")
        t = self.finish(self.post(self.b(bkt, "transfer"), {"items": [{"from": "a/", "to": "a/inner/"}]}))
        self.assertEqual(t["state"], "error")

    def test_measure_a_folder(self):
        bkt = self.bucket("measure")
        c = self.s3(bkt)
        c.put_object(bkt, "f/a", b"12345")
        c.put_object(bkt, "f/b/c", b"123")
        t = self.finish(self.post(self.b(bkt, "measure"), {"prefix": "f/"}))
        self.assertEqual(t["result"], {"objects": 2, "bytes": 8})

    def test_incomplete_multipart_uploads_can_be_listed_and_aborted(self):
        bkt = self.bucket("stale")
        c = self.s3(bkt)
        uid = c.create_multipart(bkt, "half.bin")
        ups = self.get(self.b(bkt, "uploads"))["uploads"]
        self.assertEqual([(u["key"], u["upload_id"]) for u in ups], [("half.bin", uid)])
        self.assertEqual(self.post(self.b(bkt, "uploads/abort"), {"items": ups})["aborted"], 1)
        self.assertEqual(self.get(self.b(bkt, "uploads"))["uploads"], [])

    def test_missing_object_is_a_404_not_a_crash(self):
        bkt = self.bucket("missing")
        status, body, _ = self.call("GET", f"/s3/{self.conn}/{bkt}/nope.bin", check=False)
        self.assertEqual(status, 404)
        status, body, _ = self.call("GET", self.b(bkt, "head", "key=nope.bin"), check=False)
        self.assertEqual(status, 404)

    # ---- libraries on S3
    @unittest.skipUnless(shutil.which("ffmpeg"), "ffmpeg is not installed")
    def test_s3_library_indexes_thumbnails_and_streams(self):
        bkt = self.bucket("videos")
        clip = os.path.join(self.home, "clip.mp4")
        subprocess.run(["ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc=duration=6:size=320x180:rate=24", "-pix_fmt", "yuv420p",
                        "-g", "24", "-c:v", "libx264", "-y", clip], check=True)
        with open(clip, "rb") as f:
            data = f.read()
        self.put(bkt, "shows/one/clip a.mp4", data, "video/mp4")
        self.put(bkt, "shows/two/clip b.mp4", data, "video/mp4")
        self.put(bkt, "shows/notes.txt", b"not media")
        lib = self.post("/api/libraries", {"type": "s3", "connection": self.conn, "bucket": bkt, "prefix": "shows", "name": "S3 test"})
        self.assertEqual((lib["type"], lib["location"]), ("s3", f"s3://{bkt}/shows/"))
        job = self.post("/api/index", {"id": lib["id"]})
        for _ in range(300):
            job = self.get("/api/index")["jobs"][lib["id"]]
            if job["state"] not in ("waiting", "listing", "indexing"):
                break
            time.sleep(0.2)
        self.assertEqual(job["state"], "done", job)
        items = self.get(f"/api/library?lib={lib['id']}")["items"]
        self.assertEqual(sorted(i["key"] for i in items), ["shows/one/clip a.mp4", "shows/two/clip b.mp4"])
        self.assertEqual(sorted(i["dir"] for i in items), ["one", "two"])
        for i in items:
            self.assertGreaterEqual(i["frames"], 3, i)
            self.assertAlmostEqual(i["duration"], 6, delta=0.5)
            self.assertNotIn("error", i)
            status, jpg, _ = self.call("GET", f"/thumbs/{lib['id']}/{i['id']}-{i['ver']}-0.avif")
            self.assertTrue(jpg[:2] == b"\xff\xd8" or (jpg[:4] == b"RIFF" and jpg[8:12] == b"WEBP") or jpg[4:8] == b"ftyp")
        # the stable media URL redirects to a presigned link that serves ranges
        it = items[0]
        req = urllib.request.Request(f"http://127.0.0.1:{self.port}/media/{lib['id']}/{it['id']}/x.mp4", headers={"Range": "bytes=4-7"})
        with urllib.request.urlopen(req) as r:
            self.assertEqual((r.status, r.read()), (206, b"ftyp"))

    def test_edit_library_name_and_location(self):
        bkt = self.bucket("editable")
        lib = self.post("/api/libraries", {"type": "s3", "connection": self.conn, "bucket": bkt, "prefix": "a", "name": "Editable"})
        out = self.post("/api/libraries/update", {"id": lib["id"], "name": "Renamed"})
        self.assertEqual((out["name"], out["moved"], out["location"]), ("Renamed", False, f"s3://{bkt}/a/"))
        for _ in range(50):  # let the first indexing run finish: editing is refused while one is running
            if self.get("/api/index")["jobs"].get(lib["id"], {}).get("state") == "done":
                break
            time.sleep(0.1)
        out = self.post("/api/libraries/update", {"id": lib["id"], "connection": self.conn, "bucket": bkt, "prefix": "b/c"})
        self.assertEqual((out["moved"], out["location"]), (True, f"s3://{bkt}/b/c/"))
        status, body, _ = self.call("POST", "/api/libraries/update", {"id": lib["id"], "bucket": ""}, check=False)
        self.assertEqual(status, 400)
        status, body, _ = self.call("POST", "/api/libraries/update", {"id": "nope", "name": "x"}, check=False)
        self.assertEqual(status, 400)


if __name__ == "__main__":
    unittest.main()
