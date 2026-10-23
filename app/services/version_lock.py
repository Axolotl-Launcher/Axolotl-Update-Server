import hashlib
import os
from contextlib import contextmanager
from functools import wraps
from pathlib import Path

from flask import current_app, request
from semver import Version as SemVersion

from ..errors import ApiError


@contextmanager
def version_lock(version: str):
    """Serialize filesystem mutations across Gunicorn workers on one host."""
    directory = Path(current_app.instance_path) / "version-locks"
    directory.mkdir(parents=True, exist_ok=True)
    path = directory / hashlib.sha256(version.encode()).hexdigest()
    with path.open("a+b") as lock:
        try:
            if os.name == "nt":
                import msvcrt
                if path.stat().st_size == 0:
                    lock.write(b"0")
                    lock.flush()
                lock.seek(0)
                msvcrt.locking(lock.fileno(), msvcrt.LK_NBLCK, 1)
            else:
                import fcntl
                fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError as exc:
            raise ApiError("version_busy", "Another operation is modifying this version; retry later.", 409) from exc
        try:
            yield
        finally:
            if os.name == "nt":
                lock.seek(0)
                msvcrt.locking(lock.fileno(), msvcrt.LK_UNLCK, 1)
            else:
                fcntl.flock(lock.fileno(), fcntl.LOCK_UN)


def version_operation(view):
    @wraps(view)
    def wrapped(*args, **kwargs):
        version = (request.view_args or {}).get("version")
        if version is None:
            body = request.get_json(silent=True)
            version = body.get("version") if isinstance(body, dict) else None
        if not isinstance(version, str):
            return view(*args, **kwargs)
        try:
            SemVersion.parse(version)
        except (ValueError, TypeError):
            return view(*args, **kwargs)
        with version_lock(version):
            return view(*args, **kwargs)
    return wrapped
