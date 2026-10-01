"""Signing and URL-building tests that need no network (AWS's published Signature V4 example)."""
import unittest
from datetime import datetime, timezone

from medialib.s3 import Connection, S3Client, key_path, parse_endpoint, plan_parts

AWS_KEY, AWS_SECRET = "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"


class Signing(unittest.TestCase):
    def test_presign_matches_the_aws_documentation_example(self):
        # https://docs.aws.amazon.com/AmazonS3/latest/API/sigv4-query-string-auth.html
        c = S3Client(Connection(endpoint="https://s3.amazonaws.com", access_key=AWS_KEY, secret_key=AWS_SECRET, addressing="virtual"))
        c._now = lambda: datetime(2013, 5, 24, tzinfo=timezone.utc)
        url = c.presign("GET", "examplebucket", "test.txt", 86400)
        self.assertTrue(url.startswith("https://examplebucket.s3.amazonaws.com/test.txt?"))
        self.assertTrue(url.endswith("X-Amz-Signature=aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404"), url)

    def test_header_signature_matches_the_aws_documentation_example(self):
        # GET Object with a Range header, from the "Signature Calculations for the Authorization Header" page.
        c = S3Client(Connection(endpoint="https://s3.amazonaws.com", access_key=AWS_KEY, secret_key=AWS_SECRET, addressing="virtual"))
        c._now = lambda: datetime(2013, 5, 24, tzinfo=timezone.utc)
        host, path = c._target("examplebucket", "test.txt")
        h = c._signed_headers("GET", host, path, None, {"Range": "bytes=0-9"}, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
        self.assertIn("Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41", h["authorization"])

    def test_path_style_and_special_characters(self):
        c = S3Client(Connection(endpoint="http://127.0.0.1:9000", access_key="a", secret_key="b"))
        self.assertEqual(c._target("media", "a b/é+&.mp4"), ("127.0.0.1:9000", "/media/a%20b/%C3%A9%2B%26.mp4"))
        self.assertEqual(c._target(None, None), ("127.0.0.1:9000", "/"))

    def test_virtual_style_falls_back_to_path_for_ip_hosts(self):
        c = S3Client(Connection(endpoint="http://10.0.0.5:9000", access_key="a", secret_key="b", addressing="virtual"))
        self.assertFalse(c.virtual)

    def test_endpoint_parsing(self):
        self.assertEqual(parse_endpoint("fsapi.example.com"), ("https", "fsapi.example.com", ""))
        self.assertEqual(parse_endpoint("https://x.example.com:443/"), ("https", "x.example.com", ""))
        self.assertEqual(parse_endpoint("http://nas:9000/s3/"), ("http", "nas:9000", "/s3"))
        self.assertEqual(parse_endpoint("", "eu-west-1"), ("https", "s3.eu-west-1.amazonaws.com", ""))
        with self.assertRaises(ValueError):
            parse_endpoint("https://")

    def test_key_path_keeps_slashes_only(self):
        self.assertEqual(key_path("a/b c/~d_e-f.g"), "a/b%20c/~d_e-f.g")

    def test_part_planning_stays_under_ten_thousand_parts(self):
        part, count = plan_parts(5 * 1024 ** 4, 16 * 1024 ** 2)
        self.assertLessEqual(count, 10_000)
        self.assertGreaterEqual(part, 5 * 1024 ** 2)
        self.assertEqual(plan_parts(1, 16 * 1024 ** 2), (16 * 1024 ** 2, 1))


if __name__ == "__main__":
    unittest.main()
