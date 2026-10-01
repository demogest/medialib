"""Object-storage connections: the accounts a library or the storage browser talks to.

Credentials live in config.json (git-ignored). A secret can also be read from an environment variable instead
(`secret_key_env`). The API never sends a secret back to the browser, only whether one is set.
"""
import configparser
import json
import os
import pathlib
import threading

from .config import _config_lock, save_config, unique_id
from .s3 import PROVIDERS, Connection, S3Client, S3Error, parse_endpoint
from .util import run, slug

FIELDS = ("name", "provider", "endpoint", "region", "access_key", "secret_key", "secret_key_env", "session_token",
          "addressing", "verify_tls", "default_bucket")


def find(cfg, conn_id):
    return next((c for c in cfg.get("connections", []) if c["id"] == conn_id), None)


def secret_of(conn):
    return conn.get("secret_key") or os.environ.get(conn.get("secret_key_env") or "", "")


def public(conn):
    """The connection as the browser may see it: everything but the secret."""
    out = {k: conn.get(k, "") for k in ("id", "name", "provider", "endpoint", "region", "access_key", "secret_key_env",
                                       "addressing", "default_bucket")}
    out["verify_tls"] = conn.get("verify_tls", True)
    out["has_secret"] = bool(secret_of(conn))
    out["has_token"] = bool(conn.get("session_token"))
    out["secret_source"] = "env" if conn.get("secret_key_env") and not conn.get("secret_key") else "config" if conn.get("secret_key") else ""
    return out


def to_connection(conn):
    return Connection(endpoint=conn.get("endpoint", ""), access_key=conn.get("access_key", ""), secret_key=secret_of(conn),
                      region=conn.get("region") or "us-east-1", session_token=conn.get("session_token", ""),
                      addressing=conn.get("addressing") or "path", verify_tls=conn.get("verify_tls", True))


class Clients:
    """One S3Client per connection, rebuilt when its settings change."""

    def __init__(self, cfg):
        self.cfg, self._clients, self._lock = cfg, {}, threading.Lock()

    def get(self, conn_id):
        conn = find(self.cfg, conn_id)
        if not conn:
            raise ValueError(f"Unknown connection “{conn_id}”.")
        sig = json.dumps(to_connection(conn).__dict__, sort_keys=True, default=str)
        with self._lock:
            cached = self._clients.get(conn_id)
            if cached and cached[0] == sig:
                return cached[1]
            if cached:
                cached[1].close()
            client = S3Client(to_connection(conn))
            self._clients[conn_id] = (sig, client)
            return client

    def drop(self, conn_id):
        with self._lock:
            cached = self._clients.pop(conn_id, None)
        if cached:
            cached[1].close()


def clean(data, existing=None):
    """Validate form data into a connection record. A blank secret keeps the stored one when editing."""
    existing = existing or {}
    out = {}
    for k in FIELDS:
        v = data.get(k, existing.get(k, ""))
        out[k] = v.strip() if isinstance(v, str) else v
    out["verify_tls"] = bool(data.get("verify_tls", existing.get("verify_tls", True)))
    if not out["secret_key"] and not data.get("clear_secret"):
        out["secret_key"] = existing.get("secret_key", "")
    if not out["session_token"] and not data.get("clear_secret"):
        out["session_token"] = existing.get("session_token", "")
    if out["provider"] not in PROVIDERS:
        out["provider"] = "other"
    preset = PROVIDERS[out["provider"]]
    out["region"] = out["region"] or preset["region"]
    out["addressing"] = out["addressing"] if out["addressing"] in ("path", "virtual", "auto") else preset["addressing"]
    out["endpoint"] = out["endpoint"].replace("{region}", out["region"]) or preset["endpoint"].replace("{region}", out["region"])
    if "<" in out["endpoint"]:
        raise ValueError("Replace the placeholder in the endpoint with your account's own.")
    if not out["endpoint"]:
        raise ValueError("Enter the endpoint URL of the store.")
    parse_endpoint(out["endpoint"], out["region"])  # raises ValueError when it is not a URL
    if not out["access_key"]:
        raise ValueError("Enter an access key.")
    if not (out["secret_key"] or out["secret_key_env"]):
        raise ValueError("Enter the secret key (or the name of an environment variable that holds it).")
    out["name"] = out["name"] or PROVIDERS[out["provider"]]["label"]
    return out


def add(cfg, data):
    with _config_lock:
        rec = clean(data)
        rec["id"] = unique_id(slug(data.get("id") or rec["name"]), {c["id"] for c in cfg["connections"]})
        cfg["connections"].append({"id": rec.pop("id"), **rec})
        save_config(cfg)
    return cfg["connections"][-1]


def update(cfg, conn_id, data):
    with _config_lock:
        conn = find(cfg, conn_id)
        if not conn:
            raise ValueError("Unknown connection.")
        new = clean(data, conn)
        conn.clear()
        conn.update({"id": conn_id, **new})
        save_config(cfg)
    return conn


def remove(cfg, conn_id):
    with _config_lock:
        users = [lib["name"] for lib in cfg["libraries"] if lib.get("connection") == conn_id]
        if users:
            raise ValueError("Libraries still use this connection: " + ", ".join(users))
        cfg["connections"] = [c for c in cfg["connections"] if c["id"] != conn_id]
        save_config(cfg)


def test(client, bucket=None):
    """Try the connection: list buckets (or, when the key may not list them, look at one). Returns a report."""
    import time
    started = time.time()
    report = {"ok": False, "buckets": None, "latency_ms": None, "message": ""}
    try:
        if bucket:
            client.list_objects(bucket, delimiter="/", max_keys=1)
            report["buckets"] = None
        else:
            report["buckets"] = [b["name"] for b in client.list_buckets()]
        report.update(ok=True, latency_ms=round((time.time() - started) * 1000), message="Connected.")
    except S3Error as e:
        report["message"] = str(e)
        report["code"] = e.code
        report["status"] = e.status
        if e.code in ("AccessDenied", "AllAccessDisabled") and not bucket:
            report["hint"] = "The key is valid but may not list buckets. Set a default bucket on the connection."
            report["ok"] = e.status == 403
    except ValueError as e:
        report["message"] = str(e)
    return report


# ---------------------------------------------------------------- finding credentials you already have

def rclone_remotes(cfg):
    """S3 remotes of the user's rclone setup, as connection drafts (the secret is included: it stays server side)."""
    try:
        r = run([cfg.get("rclone", "rclone"), "config", "dump"], timeout=20)
        dump = json.loads(r.stdout.decode("utf-8") or "{}") if r.returncode == 0 else {}
    except (OSError, ValueError, RuntimeError):
        return []
    out = []
    for name, c in dump.items():
        if c.get("type") != "s3":
            continue
        provider = {"AWS": "aws", "Minio": "minio", "Cloudflare": "r2", "Wasabi": "wasabi", "Alibaba": "oss",
                    "TencentCOS": "cos", "DigitalOcean": "spaces", "Backblaze": "b2", "GCS": "gcs"}.get(c.get("provider", ""), "other")
        out.append({"source": f"rclone:{name}", "label": f"rclone remote {name}", "name": name, "provider": provider,
                    "endpoint": c.get("endpoint", ""), "region": c.get("region") if c.get("region") not in ("", None) else "us-east-1",
                    "access_key": c.get("access_key_id", ""), "secret_key": c.get("secret_access_key", ""),
                    "session_token": c.get("session_token", ""),
                    "addressing": "path" if c.get("force_path_style", "true") != "false" and provider != "aws" else "virtual"})
    return out


def aws_profiles():
    """Profiles of ~/.aws/credentials (and endpoint/region from ~/.aws/config) as connection drafts."""
    home = pathlib.Path(os.path.expanduser("~")) / ".aws"
    creds, conf = configparser.RawConfigParser(), configparser.RawConfigParser()
    try:
        creds.read(os.environ.get("AWS_SHARED_CREDENTIALS_FILE") or home / "credentials", encoding="utf-8")
        conf.read(os.environ.get("AWS_CONFIG_FILE") or home / "config", encoding="utf-8")
    except (OSError, configparser.Error):
        return []
    out = []
    for name in creds.sections():
        c = dict(creds[name])
        cs = dict(conf[name] if conf.has_section(name) else conf[f"profile {name}"] if conf.has_section(f"profile {name}") else {})
        if not c.get("aws_access_key_id"):
            continue
        endpoint = cs.get("endpoint_url") or ""
        out.append({"source": f"aws:{name}", "label": f"AWS profile {name}", "name": name, "provider": "other" if endpoint else "aws",
                    "endpoint": endpoint, "region": cs.get("region", "us-east-1"), "access_key": c["aws_access_key_id"],
                    "secret_key": c.get("aws_secret_access_key", ""), "session_token": c.get("aws_session_token", ""),
                    "addressing": "path" if endpoint else "virtual"})
    return out


def environment_draft():
    key, secret = os.environ.get("AWS_ACCESS_KEY_ID"), os.environ.get("AWS_SECRET_ACCESS_KEY")
    if not (key and secret):
        return []
    endpoint = os.environ.get("AWS_ENDPOINT_URL_S3") or os.environ.get("AWS_ENDPOINT_URL") or ""
    return [{"source": "env", "label": "Environment variables (AWS_ACCESS_KEY_ID …)", "name": "Environment",
             "provider": "other" if endpoint else "aws", "endpoint": endpoint, "region": os.environ.get("AWS_REGION") or os.environ.get("AWS_DEFAULT_REGION") or "us-east-1",
             "access_key": key, "secret_key": secret, "session_token": os.environ.get("AWS_SESSION_TOKEN", ""),
             "addressing": "path" if endpoint else "virtual"}]


def importable(cfg):
    """Everything on this machine that could become a connection. Secrets are held back for the browser."""
    return rclone_remotes(cfg) + aws_profiles() + environment_draft()


def describe_draft(d):
    return {k: d[k] for k in ("source", "label", "name", "provider", "endpoint", "region", "access_key", "addressing")}


def adopt(cfg, source):
    """Make a connection out of one of importable()'s drafts (or reuse the one already made from it)."""
    draft = next((d for d in importable(cfg) if d["source"] == source), None)
    if not draft:
        raise ValueError("That credential source is no longer available.")
    for c in cfg["connections"]:
        if (c["endpoint"], c["access_key"]) == (draft["endpoint"], draft["access_key"]):
            return c
    return add(cfg, {k: v for k, v in draft.items() if k not in ("source", "label")})
