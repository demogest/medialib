"""Shared helpers for the end-to-end tests: the Go binary, a mock S3 (moto), and a small boto3 wrapper."""
import os
import pathlib
import shutil
import socket
import subprocess
import tempfile
import time

ROOT = pathlib.Path(__file__).resolve().parent.parent.parent


def binary():
    """The medialib binary: $MEDIALIB_BIN, or one built into a temp folder."""
    env = os.environ.get("MEDIALIB_BIN")
    if env:
        return env
    out = pathlib.Path(tempfile.gettempdir()) / ("medialib-e2e" + (".exe" if os.name == "nt" else ""))
    subprocess.run(["go", "build", "-o", str(out), "./cmd/medialib"], cwd=ROOT, check=True, env={**os.environ, "CGO_ENABLED": "0"})
    return str(out)


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def wait_port(port, seconds=20):
    end = time.time() + seconds
    while time.time() < end:
        try:
            socket.create_connection(("127.0.0.1", port), 0.3).close()
            return
        except OSError:
            time.sleep(0.2)
    raise RuntimeError(f"nothing listening on {port}")


class Store:
    """The few S3 calls the tests need to set up and check buckets behind medialib's back (boto3 against moto)."""

    def __init__(self, endpoint, key, secret):
        import boto3
        self.c = boto3.client("s3", endpoint_url=endpoint, aws_access_key_id=key, aws_secret_access_key=secret, region_name="us-east-1")

    def create_bucket(self, name):
        self.c.create_bucket(Bucket=name)

    def put_object(self, bucket, key, data, content_type=None):
        extra = {"ContentType": content_type} if content_type else {}
        self.c.put_object(Bucket=bucket, Key=key, Body=data, **extra)

    def get_object(self, bucket, key):
        return self.c.get_object(Bucket=bucket, Key=key)["Body"].read()

    def iter_objects(self, bucket, prefix=""):
        out = []
        for page in self.c.get_paginator("list_objects_v2").paginate(Bucket=bucket, Prefix=prefix):
            out += [{"key": o["Key"], "size": o["Size"]} for o in page.get("Contents", [])]
        return out

    def create_multipart(self, bucket, key):
        return self.c.create_multipart_upload(Bucket=bucket, Key=key)["UploadId"]
