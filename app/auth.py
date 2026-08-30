import hmac

from flask import current_app, request

from .errors import ApiError


def _bearer(expected: str, code: str):
    if not expected:
        raise ApiError("auth_not_configured", "Authentication is not configured.", 503)
    supplied = request.headers.get("Authorization", "")
    if not supplied.startswith("Bearer ") or not hmac.compare_digest(supplied[7:], expected):
        raise ApiError(code, "Authentication required.", 401)


def require_upload_token():
    supplied = request.headers.get("X-Upload-Token", "")
    expected = current_app.config["UPLOAD_TOKEN"]
    if not expected or not hmac.compare_digest(supplied, expected):
        raise ApiError("invalid_upload_token", "A valid upload token is required.", 401)


def require_admin():
    _bearer(current_app.config["ADMIN_TOKEN"], "invalid_admin_token")
