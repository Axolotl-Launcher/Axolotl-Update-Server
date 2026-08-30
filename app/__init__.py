from pathlib import Path

from flask import Flask

from .config import Config
from .extensions import db


def create_app(config_object: type[Config] = Config) -> Flask:
    app = Flask(__name__)
    app.config.from_object(config_object)
    Path(app.config["DIST_ROOT"]).mkdir(parents=True, exist_ok=True)
    db.init_app(app)

    from .routes import api_bp, latest_bp
    app.register_blueprint(api_bp)
    app.register_blueprint(latest_bp)

    with app.app_context():
        db.create_all()
    return app
