from collections import defaultdict, deque
from pathlib import Path
from time import monotonic
from typing import Deque

from flask import Flask, request

from .config import Config
from .extensions import db, migrate


def create_app(config_object: type[Config] = Config) -> Flask:
    app = Flask(__name__)
    app.config.from_object(config_object)
    if app.config.get("FLASK_ENV") == "production":
        required = ("SECRET_KEY", "WEBHOOK_SECRET", "ADMIN_TOKEN")
        if any(not app.config.get(name) or app.config.get(name) == "dev-only-change-me" for name in required):
            raise RuntimeError("Production requires non-empty SECRET_KEY, WEBHOOK_SECRET, and ADMIN_TOKEN")
    Path(app.config["DIST_ROOT"]).mkdir(parents=True, exist_ok=True)
    db.init_app(app)
    migrate.init_app(app, db)
    request_buckets: defaultdict[str, Deque[float]] = defaultdict(deque)

    from .errors import register_error_handlers
    register_error_handlers(app)

    @app.before_request
    def request_id():
        import uuid

        from flask import g
        if request.path.startswith("/api/"):
            bucket = request_buckets[request.remote_addr or "unknown"]
            now = monotonic()
            while bucket and now - bucket[0] >= 60:
                bucket.popleft()
            if len(bucket) >= app.config.get("RATE_LIMIT_PER_MINUTE", 120):
                from .errors import ApiError
                raise ApiError("rate_limited", "Too many requests; retry later.", 429)
            bucket.append(now)
        rid = request.headers.get("X-Request-ID") or str(uuid.uuid4())
        g.request_id = rid
        request.environ["request_id"] = rid

    @app.after_request
    def record_usage(response):
        from .models import UsageEvent, utcnow

        # Statistics are intentionally not self-referential. Caddy-served files
        # bypass Flask; requests reaching this fallback are still recorded.
        if request.path != "/api/admin/stats":
            event_type = "download" if request.path.startswith("/dist/") else ("api" if request.path.startswith("/api/") else None)
            if event_type:
                channel = request.headers.get("X-Axolotl-Channel") or request.args.get("channel") or "unknown"
                if channel not in ("release", "beta"):
                    channel = "unknown"
                try:
                    db.session.add(UsageEvent(path=request.path, channel=channel, event_type=event_type, status_code=response.status_code, bytes_sent=response.content_length or 0, occurred_at=utcnow()))
                    db.session.commit()
                except Exception:
                    db.session.rollback()
                    app.logger.exception("Failed to record usage event")
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
