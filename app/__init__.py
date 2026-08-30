from pathlib import Path

from flask import Flask, request

from .config import Config
from .extensions import db


def create_app(config_object: type[Config] = Config) -> Flask:
    app = Flask(__name__)
    app.config.from_object(config_object)
    Path(app.config["DIST_ROOT"]).mkdir(parents=True, exist_ok=True)
    db.init_app(app)

    from .errors import register_error_handlers
    register_error_handlers(app)

    @app.before_request
    def request_id():
        import uuid

        from flask import g
        rid = request.headers.get("X-Request-ID") or str(uuid.uuid4())
        g.request_id = rid
        request.environ["request_id"] = rid

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
        db.create_all()
    return app
