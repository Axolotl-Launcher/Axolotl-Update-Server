from collections import defaultdict, deque
from pathlib import Path
from time import monotonic
from typing import Deque

from flask import Flask, request

from .config import Config
from .extensions import db, migrate
from .services.client_ip import DEFAULT_TRUSTED_PROXY_CIDRS, ClientIPMiddleware
from .services.latest_cache import LatestCache


def create_app(config_object: type[Config] = Config) -> Flask:
    app = Flask(__name__)
    app.config.from_object(config_object)
    trusted_proxies = app.config.get("TRUSTED_PROXY_CIDRS", DEFAULT_TRUSTED_PROXY_CIDRS)
    if isinstance(trusted_proxies, str):
        trusted_proxies = trusted_proxies.split(",")
    app.wsgi_app = ClientIPMiddleware(app.wsgi_app, trusted_proxies)  # type: ignore[method-assign]
    app.extensions["latest_cache"] = LatestCache(Path(app.instance_path) / "latest-cache-revision")
    if app.config.get("FLASK_ENV") == "production":
        required = ("SECRET_KEY", "WEBHOOK_SECRET", "ADMIN_TOKEN")
        if any(not app.config.get(name) or app.config.get(name) == "dev-only-change-me" for name in required):
            raise RuntimeError("Production requires non-empty SECRET_KEY, WEBHOOK_SECRET, and ADMIN_TOKEN")
    Path(app.config["DIST_ROOT"]).mkdir(parents=True, exist_ok=True)
    db.init_app(app)
    migrate.init_app(app, db)
    from .commands import register_commands
    register_commands(app)
    request_buckets: defaultdict[str, Deque[float]] = defaultdict(deque)

    from .errors import register_error_handlers
    register_error_handlers(app)

    @app.before_request
    def request_id():
        import uuid

        from flask import g
        rid = request.headers.get("X-Request-ID") or str(uuid.uuid4())
        g.request_id = rid
        request.environ["request_id"] = rid
        if request.path.startswith("/api/"):
            bucket = request_buckets[request.remote_addr or "unknown"]
            now = monotonic()
            while bucket and now - bucket[0] >= 60:
                bucket.popleft()
            if len(bucket) >= app.config.get("RATE_LIMIT_PER_MINUTE", 120):
                from .errors import ApiError
                raise ApiError("rate_limited", "Too many requests; retry later.", 429)
            bucket.append(now)

    @app.after_request
    def allow_public_download_catalog(response):
        if request.path.startswith("/api/downloads/") and request.method in {"GET", "HEAD", "OPTIONS"}:
            response.headers["Access-Control-Allow-Origin"] = "*"
            response.headers["Access-Control-Allow-Methods"] = "GET, HEAD, OPTIONS"
            response.headers["Access-Control-Allow-Headers"] = "Content-Type"
            response.headers["Access-Control-Expose-Headers"] = "ETag, X-Request-ID"
        return response

    @app.after_request
    def add_request_id(response):
        from flask import g
        response.headers["X-Request-ID"] = g.request_id
        return response

    from .routes import api_bp, latest_bp
    app.register_blueprint(api_bp)
    app.register_blueprint(latest_bp)

    with app.app_context():
        from . import models  # noqa: F401
        if app.config.get("FLASK_ENV") != "production":
            db.create_all()
    return app
