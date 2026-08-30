import hashlib
import os
import time
from pathlib import Path
from typing import Any
from urllib.parse import quote, urlparse
from urllib.request import HTTPRedirectHandler, Request, build_opener

from flask import current_app

from ..errors import ApiError

CHUNK_SIZE = 1024 * 1024
GITHUB_OWNER = "Mystic-Stars"
GITHUB_REPOSITORY = "Axolotl"


def _sha256(value: str) -> str:
    if not isinstance(value, str) or not value.startswith("sha256:"):
        raise ApiError("invalid_github_digest", "GitHub asset digest must be SHA-256.")
    digest = value.removeprefix("sha256:").lower()
    if len(digest) != 64 or any(character not in "0123456789abcdef" for character in digest):
        raise ApiError("invalid_github_digest", "GitHub asset digest must be SHA-256.")
    return digest


def validate_asset_url(url: str, tag: str, filename: str) -> None:
    parsed = urlparse(url)
    expected_path = "/" + "/".join(
        (GITHUB_OWNER, GITHUB_REPOSITORY, "releases", "download", quote(tag, safe=""), quote(filename, safe=""))
    )
    if parsed.scheme != "https" or parsed.hostname != "github.com" or parsed.path != expected_path:
        raise ApiError("invalid_github_asset_url", "GitHub asset URL is not an allowed release download URL.")
    if parsed.query or parsed.fragment or parsed.username or parsed.password or parsed.port:
        raise ApiError("invalid_github_asset_url", "GitHub asset URL contains unsupported components.")


def _open_stream(url: str):
    opener = current_app.config.get("GITHUB_DOWNLOAD_OPEN")
    timeout = current_app.config["GITHUB_DOWNLOAD_CONNECT_TIMEOUT_SECONDS"]
    if opener:
        return opener(url, timeout)
    class LimitedRedirects(HTTPRedirectHandler):
        max_redirections = 3
    client = build_opener(LimitedRedirects)
    return client.open(Request(url, headers={"User-Agent": "Axolotl-Update-Server"}), timeout=timeout)


def download_file(url: str, destination: Path, expected_size: int, expected_sha256: str) -> None:
    maximum = current_app.config["GITHUB_DOWNLOAD_MAX_SIZE"]
    if expected_size < 0 or expected_size > maximum:
        raise ApiError("github_asset_too_large", "GitHub asset exceeds the configured download limit.")
    retries = current_app.config["GITHUB_DOWNLOAD_RETRIES"]
    temporary = destination.with_name(f"{destination.name}.tmp")
    last_error: Exception | None = None
    for attempt in range(retries + 1):
        hasher = hashlib.sha256()
        size = 0
        try:
            with _open_stream(url) as response, temporary.open("wb") as output:
                while chunk := response.read(CHUNK_SIZE):
                    size += len(chunk)
                    if size > maximum:
                        raise ApiError("github_asset_too_large", "GitHub asset exceeds the configured download limit.")
                    hasher.update(chunk)
                    output.write(chunk)
                output.flush()
                os.fsync(output.fileno())
            if size != expected_size or hasher.hexdigest() != expected_sha256:
                raise ApiError("github_asset_integrity_failed", "GitHub asset metadata does not match downloaded content.")
            os.replace(temporary, destination)
            return
        except ApiError:
            temporary.unlink(missing_ok=True)
            raise
        except OSError as exc:
            temporary.unlink(missing_ok=True)
            last_error = exc
            if attempt < retries:
                time.sleep(2**attempt)
    raise ApiError("github_asset_download_failed", "GitHub asset download failed.", 502) from last_error


def prepare_catalog(release: dict[str, Any], catalog: dict[str, Any], version: str, tag: str) -> tuple[list[dict[str, Any]], dict[str, dict[str, Any]]]:
    if release.get("draft") is not False or release.get("tag_name") != tag:
        raise ApiError("invalid_github_release", "GitHub Release must be public and match the webhook tag.")
    if catalog.get("version") != version or not isinstance(catalog.get("files"), list) or not isinstance(catalog.get("artifacts"), list):
        raise ApiError("invalid_catalog", "Catalog version, files, and artifacts are required.")
    assets: dict[str, dict[str, Any]] = {}
    for asset in release.get("assets", []):
        if not isinstance(asset, dict) or not isinstance(asset.get("name"), str) or asset["name"] in assets:
            raise ApiError("invalid_github_release", "GitHub Release assets must have unique names.")
        assets[asset["name"]] = asset
    files: dict[str, dict[str, Any]] = {}
    for file_info in catalog["files"]:
        if not isinstance(file_info, dict) or not isinstance(file_info.get("filename"), str):
            raise ApiError("invalid_catalog", "Every catalog file needs a filename.")
        filename = file_info["filename"]
        asset = assets.get(filename)
        if filename in files or not asset:
            raise ApiError("invalid_catalog", "Catalog file does not match a unique GitHub asset.")
        digest = _sha256(asset.get("digest", ""))
        if file_info.get("size") != asset.get("size") or file_info.get("sha256", "").lower() != digest:
            raise ApiError("github_asset_metadata_mismatch", "Catalog file metadata does not match GitHub Release.")
        url = asset.get("browser_download_url")
        if not isinstance(url, str) or file_info.get("downloadUrl") != url:
            raise ApiError("github_asset_metadata_mismatch", "Catalog URL does not match GitHub Release.")
        validate_asset_url(url, tag, filename)
        files[filename] = {"filename": filename, "size": asset["size"], "sha256": digest, "url": url}
    return catalog["artifacts"], files
