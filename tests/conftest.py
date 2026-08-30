import tempfile

import pytest

from app import create_app
from app.extensions import db


@pytest.fixture()
def app():
    root = tempfile.mkdtemp()

    class TestConfig:
        TESTING = True
        SECRET_KEY = "test"
        SQLALCHEMY_DATABASE_URI = "sqlite:///:memory:"
        SQLALCHEMY_TRACK_MODIFICATIONS = False
        DIST_ROOT = root
        PUBLIC_BASE_URL = "https://update.axlmc.org"
        UPLOAD_TOKEN = "upload"
        ADMIN_TOKEN = "admin"
        WEBHOOK_SECRET = "webhook"
        WEBHOOK_MAX_AGE_SECONDS = 300
        MAX_CONTENT_LENGTH = 1024 * 1024

    application = create_app(TestConfig)
    with application.app_context():
        db.drop_all()
        db.create_all()
    yield application


@pytest.fixture()
def client(app):
    return app.test_client()
