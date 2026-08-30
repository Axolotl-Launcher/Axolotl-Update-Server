import os
from pathlib import Path


class Config:
    FLASK_ENV = os.getenv("FLASK_ENV", "development")
    SECRET_KEY = os.getenv("SECRET_KEY", "dev-only-change-me")
    SQLALCHEMY_DATABASE_URI = os.getenv("DATABASE_URL", "sqlite:///update-server.db")
    SQLALCHEMY_TRACK_MODIFICATIONS = False
    DIST_ROOT = str(Path(os.getenv("DIST_ROOT", "./dist")).resolve())
    PUBLIC_BASE_URL = os.getenv("PUBLIC_BASE_URL", "https://update.axlmc.org").rstrip("/")
    UPDATE_SERVER_HOST = os.getenv("UPDATE_SERVER_HOST", "127.0.0.1")
    UPDATE_SERVER_PORT = int(os.getenv("UPDATE_SERVER_PORT", "8082"))
    UPLOAD_TOKEN = os.getenv("UPLOAD_TOKEN", "")
    WEBHOOK_SECRET = os.getenv("WEBHOOK_SECRET", "")
    ADMIN_TOKEN = os.getenv("ADMIN_TOKEN", "")
    MAX_CONTENT_LENGTH = int(os.getenv("MAX_UPLOAD_SIZE", str(512 * 1024 * 1024)))
    WEBHOOK_MAX_AGE_SECONDS = int(os.getenv("WEBHOOK_MAX_AGE_SECONDS", "300"))
