import os
from pathlib import Path

from dotenv import load_dotenv

load_dotenv()


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
    RATE_LIMIT_PER_MINUTE = int(os.getenv("RATE_LIMIT_PER_MINUTE", "120"))
    RELEASE_RETENTION_COUNT = int(os.getenv("RELEASE_RETENTION_COUNT", "3"))
    BETA_RETENTION_COUNT = int(os.getenv("BETA_RETENTION_COUNT", "3"))
    GITHUB_DOWNLOAD_CONNECT_TIMEOUT_SECONDS = int(os.getenv("GITHUB_DOWNLOAD_CONNECT_TIMEOUT_SECONDS", "10"))
    GITHUB_DOWNLOAD_READ_TIMEOUT_SECONDS = int(os.getenv("GITHUB_DOWNLOAD_READ_TIMEOUT_SECONDS", "60"))
    GITHUB_DOWNLOAD_RETRIES = int(os.getenv("GITHUB_DOWNLOAD_RETRIES", "3"))
    GITHUB_DOWNLOAD_MAX_SIZE = int(os.getenv("GITHUB_DOWNLOAD_MAX_SIZE", str(1024 * 1024 * 1024)))
