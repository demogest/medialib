"""Connection records: validation, secrets, and what the browser may see."""
import os
import unittest
from unittest import mock

from medialib import connections as C


class Records(unittest.TestCase):
    def draft(self, **kw):
        return {"provider": "minio", "endpoint": "http://nas:9000", "access_key": "AK", "secret_key": "SK", **kw}

    def test_clean_fills_the_preset_and_requires_credentials(self):
        rec = C.clean(self.draft())
        self.assertEqual((rec["addressing"], rec["region"], rec["name"]), ("path", "us-east-1", "MinIO"))
        with self.assertRaises(ValueError):
            C.clean(self.draft(secret_key=""))
        with self.assertRaises(ValueError):
            C.clean(self.draft(access_key=""))
        with self.assertRaises(ValueError):
            C.clean(self.draft(endpoint=""))

    def test_aws_preset_builds_the_regional_endpoint(self):
        rec = C.clean({"provider": "aws", "region": "eu-west-1", "access_key": "AK", "secret_key": "SK"})
        self.assertEqual((rec["endpoint"], rec["addressing"]), ("https://s3.eu-west-1.amazonaws.com", "virtual"))

    def test_placeholder_endpoints_are_refused(self):
        with self.assertRaises(ValueError):
            C.clean({"provider": "r2", "access_key": "AK", "secret_key": "SK"})

    def test_blank_secret_keeps_the_stored_one_when_editing(self):
        existing = C.clean(self.draft())
        rec = C.clean(self.draft(secret_key="", name="Renamed"), existing)
        self.assertEqual((rec["secret_key"], rec["name"]), ("SK", "Renamed"))

    def test_public_view_never_contains_the_secret(self):
        pub = C.public({"id": "x", **C.clean(self.draft(session_token="TOKEN"))})
        self.assertNotIn("SK", str(pub))
        self.assertNotIn("TOKEN", str(pub))
        self.assertTrue(pub["has_secret"] and pub["has_token"])

    def test_secret_can_come_from_the_environment(self):
        rec = C.clean(self.draft(secret_key="", secret_key_env="MEDIALIB_TEST_SECRET"))
        with mock.patch.dict(os.environ, {"MEDIALIB_TEST_SECRET": "from-env"}):
            self.assertEqual(C.to_connection(rec).secret_key, "from-env")
            self.assertEqual(C.public({"id": "x", **rec})["secret_source"], "env")

    def test_a_connection_in_use_cannot_be_removed(self):
        cfg = {"connections": [{"id": "a"}], "libraries": [{"name": "Lib", "connection": "a"}]}
        with mock.patch.object(C, "save_config"):
            with self.assertRaises(ValueError):
                C.remove(cfg, "a")
            cfg["libraries"] = []
            C.remove(cfg, "a")
        self.assertEqual(cfg["connections"], [])


if __name__ == "__main__":
    unittest.main()
