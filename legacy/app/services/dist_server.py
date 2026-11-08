from urllib.parse import quote, urlsplit, urlunsplit

from flask import request

from ..errors import ApiError


def _same_endpoint(first, second):
    first_port = first.port or (443 if first.scheme == "https" else 80)
    second_port = second.port or (443 if second.scheme == "https" else 80)
    return (
        first.hostname and second.hostname
        and first.hostname.lower().rstrip(".") == second.hostname.lower().rstrip(".")
        and (first_port == second_port or {first_port, second_port} <= {80, 443})
    )


def normalize_dist_server(value: str, public_base_url: str) -> str:
    value = value.strip()
    if not value:
        return ""
    value = value.rstrip("/")
    try:
        parsed = urlsplit(value)
        # Reading port also validates malformed/non-numeric port values.
        _ = parsed.port
        if (
            parsed.scheme not in ("http", "https") or not parsed.hostname
            or parsed.username is not None or parsed.password is not None
            or parsed.query or parsed.fragment or "\\" in value
            or any(character.isspace() or ord(character) < 32 or ord(character) == 127 for character in value)
            or any(part in (".", "..") for part in parsed.path.split("/"))
        ):
            raise ValueError
        public = urlsplit(public_base_url.strip().rstrip("/"))
        if _same_endpoint(parsed, public) and parsed.path == public.path:
            raise ValueError
    except (ValueError, TypeError) as exc:
        raise ValueError("DIST_SERVER must be an absolute HTTP(S) base URL without credentials/query/fragment and must not equal PUBLIC_BASE_URL.") from exc
    return value


def dist_destination(server: str, version: str, filename: str) -> str:
    for component in (version, *filename.split("/")):
        if (
            not component or component in (".", "..") or "\\" in component
            or any(ord(character) < 32 or ord(character) == 127 for character in component)
        ):
            raise ApiError("invalid_dist_path", "Distribution path contains unsafe components.")
    parsed = urlsplit(server)
    # Catch aliases serving this same application as well as the public host.
    if _same_endpoint(parsed, urlsplit(request.host_url)) and not parsed.path:
        raise ApiError("dist_server_loop", "DIST_SERVER points back to this distribution endpoint.", 503)
    path = f"{parsed.path}/dist/{quote(version, safe='')}/{quote(filename, safe='/')}"
    query = request.query_string.decode("latin-1")
    return urlunsplit((parsed.scheme, parsed.netloc, path, query, ""))
