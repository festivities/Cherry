"""Operator documents on the Cherry staff API (loopback only; PLAN Roadmap 8, P3, slice 8.5).

A published document applies at the NEXT restart of Cherry; nothing running changes when it is published.

  cherry-docs.py live                          every document: source, published version, pending restart
  cherry-docs.py history NAME                  stored versions, newest first (no bodies)
  cherry-docs.py show NAME VERSION             one version's JSON
  cherry-docs.py draft NAME FILE [--note T]    store FILE as the next draft
  cherry-docs.py validate NAME VERSION         the validator's verdict (changes nothing)
  cherry-docs.py publish NAME VERSION          publish it (refused when it fails validation)
  cherry-docs.py rollback NAME                 re-publish the version it replaced, or un-publish

Options: --admin URL (default http://127.0.0.1:8099). Standard library only. The file is sent as raw bytes.
"""

import argparse
import json
import sys
import urllib.error
import urllib.parse
import urllib.request


def call(base, method, path, body=None):
    req = urllib.request.Request(base + path, data=body, method=method)
    if body is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as err:
        return err.code, err.read()


def show(status, raw):
    try:
        doc = json.loads(raw.decode("utf-8"))
    except ValueError:
        doc = {"raw": raw.decode("utf-8", "replace")}
    print(json.dumps(doc, indent=2, ensure_ascii=False))
    return 0 if status < 400 else 1


def main():
    p = argparse.ArgumentParser(description="Operator documents on the Cherry staff API.")
    p.add_argument("--admin", default="http://127.0.0.1:8099", help="the admin listener's base URL")
    sub = p.add_subparsers(dest="cmd", required=True)
    sub.add_parser("live")
    sub.add_parser("history").add_argument("name")
    s = sub.add_parser("show")
    s.add_argument("name")
    s.add_argument("version", type=int)
    d = sub.add_parser("draft")
    d.add_argument("name")
    d.add_argument("file")
    d.add_argument("--note", default="")
    for cmd in ("validate", "publish"):
        v = sub.add_parser(cmd)
        v.add_argument("name")
        v.add_argument("version", type=int)
    sub.add_parser("rollback").add_argument("name")
    args = p.parse_args()

    name = urllib.parse.quote(getattr(args, "name", ""), safe="")
    if args.cmd == "live":
        return show(*call(args.admin, "GET", "/admin/docs"))
    if args.cmd == "history":
        return show(*call(args.admin, "GET", f"/admin/docs/{name}"))
    if args.cmd == "show":
        return show(*call(args.admin, "GET", f"/admin/docs/{name}/{args.version}"))
    if args.cmd == "draft":
        with open(args.file, "rb") as f:
            body = f.read()
        query = urllib.parse.urlencode({"note": args.note})
        return show(*call(args.admin, "POST", f"/admin/docs/{name}?{query}", body))
    if args.cmd == "validate":
        return show(*call(args.admin, "POST", f"/admin/docs/{name}/{args.version}/validate"))
    if args.cmd == "publish":
        return show(*call(args.admin, "POST", f"/admin/docs/{name}/{args.version}/publish"))
    if args.cmd == "rollback":
        return show(*call(args.admin, "POST", f"/admin/docs/{name}/rollback"))
    return 2


if __name__ == "__main__":
    sys.exit(main())
