"""
Factorio mod manager.

Assembles the contents of /factorio/mods from two sources:

  * Private mods vendored in the repo at mods/private/*.zip (COPYied into the
    image at build time).
  * Public mods listed by name in mods/public.json. On boot we resolve the
    latest release of each from the Factorio mod portal, then serve its zip
    from the S3 cache (s3://$AWS_BUCKET_NAME/mods/) if we already have that
    version, otherwise download it and populate the cache. So each version is
    downloaded from the portal at most once, but you always run the latest.

It also writes mod-list.json so Factorio enables the base game and every
private + public mod (built-in/DLC mods are enabled but never downloaded).

NOTE: always running the latest release means a mod update can land under an
existing save on the next restart. Factorio migrates most saves across mod
updates, but a breaking update can desync — keep the S3 save backups in mind.

Usage:
    python3 mod_manager.py --sync   # assemble /factorio/mods for launch
"""

import json
import os
import sys
import urllib.parse
import urllib.request
from argparse import ArgumentParser
from datetime import datetime, timezone
from typing import Optional

sys.path.insert(0, os.path.dirname(__file__))

MODS_DIR = "/factorio/mods"
PRIVATE_DIR = os.path.join(os.path.dirname(__file__), os.pardir, "mods", "private")
PUBLIC_JSON = os.path.join(os.path.dirname(__file__), os.pardir, "mods", "public.json")

# Mods shipped with the base game / DLC. These are provided by the Factorio
# image itself, so they must be enabled in mod-list.json but never downloaded
# from the portal (the portal has no downloadable zip for them).
BUILTIN_MODS = {"base", "space-age", "quality", "elevated-rails"}

PORTAL_API = "https://mods.factorio.com/api/mods"
PORTAL_BASE = "https://mods.factorio.com"

# The mod portal sits behind Cloudflare, which blocks the default
# "Python-urllib/x.y" User-Agent with a 403 (error code 1010). Any explicit
# User-Agent gets through.
USER_AGENT = "factorio-server-mod-manager/1.0"


def portal_get(url: str, timeout: int):
    """Open a mod-portal URL with a User-Agent Cloudflare will accept."""
    request = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
    return urllib.request.urlopen(request, timeout=timeout)


def log(message: str) -> None:
    timestamp = datetime.now(tz=timezone.utc).strftime("%Y-%m-%d %H:%M:%S UTC")
    print(f"[{timestamp}] [mod_manager] {message}")


def parse_arguments() -> ArgumentParser:
    parser = ArgumentParser("Factorio mod manager")
    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument("--sync", action="store_true", help="Assemble /factorio/mods for launch")
    group.add_argument(
        "--check-updates",
        action="store_true",
        help="Report newer portal releases without applying them",
    )
    return parser


def load_public_mods() -> list[str]:
    """Return the list of public mod names from public.json.

    public.json holds a plain array of mod names; we always fetch the latest
    release of each from the portal.
    """
    if not os.path.exists(PUBLIC_JSON):
        log(f"No public mod list at {PUBLIC_JSON}, skipping public mods")
        return []
    with open(PUBLIC_JSON) as handle:
        data = json.load(handle)
    mods = data.get("mods", [])
    names: list[str] = []
    for entry in mods:
        if not isinstance(entry, str):
            log(f"FATAL: public.json entries must be mod-name strings, got: {entry!r}")
            sys.exit(1)
        names.append(entry)
    return names


def factorio_credentials() -> tuple[str, str]:
    """Return (username, token) for the mod portal.

    Prefers dedicated env vars, falling back to the values already present in
    server-settings.json (the same factorio.com account credentials work for
    the mod portal download endpoint).
    """
    username = os.environ.get("FACTORIO_USERNAME")
    token = os.environ.get("FACTORIO_TOKEN")
    if username and token:
        return username, token

    settings_path = "/factorio/config/server-settings.json"
    try:
        with open(settings_path) as handle:
            settings = json.load(handle)
        username = username or settings.get("username")
        token = token or settings.get("token")
    except (OSError, json.JSONDecodeError) as error:
        log(f"WARNING: could not read credentials from {settings_path}: {error}")

    if not (username and token):
        log(
            "FATAL: Factorio portal credentials missing. Set FACTORIO_USERNAME + "
            "FACTORIO_TOKEN, or username/token in server-settings.json."
        )
        sys.exit(1)
    return username, token


def get_s3():
    from s3fs import S3FileSystem

    key = os.environ.get("AWS_ACCESS_KEY_ID")
    secret = os.environ.get("AWS_SECRET_ACCESS_KEY")
    endpoint_url = os.environ.get("AWS_ENDPOINT_URL_S3")
    bucket = os.environ.get("AWS_BUCKET_NAME")
    if not all([key, secret, endpoint_url, bucket]):
        log("FATAL: Missing S3 environment variables (AWS_ACCESS_KEY_ID/SECRET/ENDPOINT/BUCKET)")
        sys.exit(1)
    fs = S3FileSystem(key=key, secret=secret, endpoint_url=endpoint_url)
    return fs, bucket


def portal_releases(name: str) -> list[dict]:
    """Fetch the release list for a mod from the portal API."""
    url = f"{PORTAL_API}/{urllib.parse.quote(name)}/full"
    with portal_get(url, timeout=30) as response:
        data = json.load(response)
    return data.get("releases", [])


def latest_release(releases: list[dict]) -> Optional[dict]:
    """Return the newest release. The portal lists releases chronologically,
    so the last entry is the latest published version."""
    return releases[-1] if releases else None


def download_from_portal(release: dict, username: str, token: str, dest_path: str) -> None:
    """Download a single release zip from the portal to dest_path."""
    download_url = release["download_url"]  # e.g. /download/mod-name/<id>
    query = urllib.parse.urlencode({"username": username, "token": token})
    url = f"{PORTAL_BASE}{download_url}?{query}"
    log(f"Downloading from portal: {download_url}")
    with portal_get(url, timeout=300) as response:
        with open(dest_path, "wb") as out:
            out.write(response.read())


def sync_public_mod(
    name: str,
    fs,
    bucket: str,
    username: str,
    token: str,
) -> str:
    """Ensure the latest release of one public mod is present in MODS_DIR.
    Returns its internal name.

    We always resolve the latest version from the portal, then serve its zip
    from the S3 cache if we already have it, otherwise download and cache it.
    """
    releases = portal_releases(name)
    release = latest_release(releases)
    if release is None:
        log(f"FATAL: {name} has no releases on the portal")
        sys.exit(1)

    version = release["version"]
    filename = f"{name}_{version}.zip"
    local_path = os.path.join(MODS_DIR, filename)
    cache_url = f"{bucket}/mods/{filename}"

    if fs.exists(cache_url):
        log(f"Cache hit: {name} latest={version} <- s3://{cache_url}")
        with fs.open(cache_url, "rb") as src, open(local_path, "wb") as dst:
            dst.write(src.read())
        return name

    log(f"Cache miss: {name} latest={version}, fetching from portal")
    download_from_portal(release, username, token, local_path)

    # Populate the cache for next boot.
    log(f"Caching to s3://{cache_url}")
    with open(local_path, "rb") as src, fs.open(cache_url, "wb") as dst:
        dst.write(src.read())
    return name


def clean_mods_dir() -> None:
    """Remove any previously-synced zips so a shrinking mod list can't leave
    orphans behind (which Factorio would still load)."""
    os.makedirs(MODS_DIR, exist_ok=True)
    for fname in os.listdir(MODS_DIR):
        if fname.endswith(".zip"):
            os.remove(os.path.join(MODS_DIR, fname))


def copy_private_mods() -> list[str]:
    """Copy vendored private mods into MODS_DIR. Returns their internal names."""
    names: list[str] = []
    private_dir = os.path.abspath(PRIVATE_DIR)
    if not os.path.isdir(private_dir):
        return names
    for fname in sorted(os.listdir(private_dir)):
        if not fname.endswith(".zip"):
            continue
        src = os.path.join(private_dir, fname)
        dst = os.path.join(MODS_DIR, fname)
        with open(src, "rb") as s, open(dst, "wb") as d:
            d.write(s.read())
        # Mod internal name is the filename minus the trailing _<version>.zip
        stem = fname[:-4]
        internal = stem.rsplit("_", 1)[0] if "_" in stem else stem
        names.append(internal)
        log(f"Copied private mod: {fname} (name={internal})")
    return names


def write_mod_list(enabled_names: list[str]) -> None:
    """Write mod-list.json enabling base + every supplied mod name."""
    ordered: list[str] = []
    for name in ["base", *enabled_names]:
        if name not in ordered:
            ordered.append(name)
    mod_list = {"mods": [{"name": name, "enabled": True} for name in ordered]}
    path = os.path.join(MODS_DIR, "mod-list.json")
    with open(path, "w") as handle:
        json.dump(mod_list, handle, indent=2)
    log(f"Wrote {path} with {len(ordered)} mods enabled: {', '.join(ordered)}")


def cmd_sync() -> None:
    public_names = load_public_mods()
    clean_mods_dir()

    enabled: list[str] = []

    # Built-in mods (base game + DLC) are enabled but never downloaded — the
    # portal has no downloadable zip for them.
    downloadable = [n for n in public_names if n not in BUILTIN_MODS]
    builtin = [n for n in public_names if n in BUILTIN_MODS]
    enabled.extend(builtin)

    if downloadable:
        fs, bucket = get_s3()
        username, token = factorio_credentials()
        for name in downloadable:
            enabled.append(sync_public_mod(name, fs, bucket, username, token))

    enabled.extend(copy_private_mods())
    write_mod_list(enabled)
    log("Mod sync complete")


def cmd_check_updates() -> None:
    # public.json lists mod names only and --sync always fetches the latest
    # release, so there is nothing to reconcile here.
    log("public.json tracks latest releases; nothing to check (--sync always pulls latest)")


def main() -> None:
    args = parse_arguments().parse_args()
    if args.sync:
        cmd_sync()
    elif args.check_updates:
        cmd_check_updates()


if __name__ == "__main__":
    main()
