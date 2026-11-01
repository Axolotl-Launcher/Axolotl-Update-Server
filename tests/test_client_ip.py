import logging

import pytest

from app.extensions import db
from app.models import AuditLog, Version, utcnow
from app.services.client_ip import ClientIPMiddleware


@pytest.mark.parametrize("peer,headers,expected", [
    ("127.0.0.1", {"X-Forwarded-For": "203.0.113.12"}, "203.0.113.12"),
    ("::1", {"X-Forwarded-For": "2001:db8::12"}, "2001:db8::12"),
    ("::ffff:127.0.0.1", {"X-Forwarded-For": "203.0.113.12"}, "203.0.113.12"),
    ("127.0.0.1", {"X-Forwarded-For": "203.0.113.12, 172.64.0.10", "CF-Connecting-IP": "203.0.113.12"}, "203.0.113.12"),
    ("127.0.0.1", {"X-Forwarded-For": "198.51.100.9, 203.0.113.12, 172.64.0.10"}, "203.0.113.12"),
    ("172.64.0.10", {"CF-Connecting-IP": "2001:db8::12"}, "2001:db8::12"),
    ("2606:4700::10", {"X-Forwarded-For": "2001:db8::12"}, "2001:db8::12"),
    ("203.0.113.44", {"X-Forwarded-For": "198.51.100.9", "CF-Connecting-IP": "198.51.100.9", "X-Real-IP": "198.51.100.9"}, "203.0.113.44"),
    ("127.0.0.1", {"X-Forwarded-For": "203.0.113.44", "CF-Connecting-IP": "198.51.100.9"}, "203.0.113.44"),
    ("127.0.0.1", {"CF-Connecting-IP": "198.51.100.9"}, "127.0.0.1"),
    ("127.0.0.1", {"X-Forwarded-For": "172.64.0.10, 203.0.113.44", "CF-Connecting-IP": "198.51.100.9"}, "203.0.113.44"),
    ("127.0.0.1", {"X-Forwarded-For": "not-an-ip"}, "127.0.0.1"),
    ("127.0.0.1", {"X-Forwarded-For": "203.0.113.12, "}, "127.0.0.1"),
    ("127.0.0.1", {"X-Forwarded-For": ",".join(["203.0.113.12"] * 65)}, "127.0.0.1"),
    ("127.0.0.1", {"X-Forwarded-For": "203.0.113.12, 172.64.0.10", "CF-Connecting-IP": "bad"}, "203.0.113.12"),
    ("127.0.0.1", {}, "127.0.0.1"),
])
def test_real_client_ip_in_audit_without_duplicate_app_log(client, app, caplog, peer, headers, expected):
    with app.app_context():
        db.session.add(Version(version="1.9.4", channel="release", status="published", published_at=utcnow()))
        db.session.commit()
    with caplog.at_level(logging.INFO, logger=app.logger.name):
        response = client.post(
            "/api/admin/versions/1.9.4/revoke?secret=not-logged",
            headers={**headers, "Authorization": "Bearer admin"},
            environ_overrides={"REMOTE_ADDR": peer}, json={"reason": "test"},
        )
    assert response.status_code == 200
    with app.app_context():
        assert AuditLog.query.one().ip_address == expected
    assert response.headers["X-Request-ID"]
    assert not any(record.getMessage().startswith("request ") for record in caplog.records)


def test_rate_limit_is_per_resolved_client(client, app):
    app.config["RATE_LIMIT_PER_MINUTE"] = 1
    assert client.get("/api/health", headers={"X-Forwarded-For": "203.0.113.1"}).status_code == 200
    assert client.get("/api/health", headers={"X-Forwarded-For": "203.0.113.2"}).status_code == 200
    response = client.get("/api/health", headers={"X-Forwarded-For": "203.0.113.1"})
    assert response.status_code == 429 and response.headers["X-Request-ID"]


def test_custom_trust_and_disabled_forwarding():
    environ = {"REMOTE_ADDR": "10.0.0.2", "HTTP_X_FORWARDED_FOR": "203.0.113.12"}
    assert ClientIPMiddleware(None, ["10.0.0.2/32"]).resolve(environ) == "203.0.113.12"
    assert ClientIPMiddleware(None, []).resolve(environ) == "10.0.0.2"
    # Explicit overrides must also control whether a CF hop is trusted.
    environ = {"REMOTE_ADDR": "127.0.0.1", "HTTP_X_FORWARDED_FOR": "203.0.113.12, 172.64.0.10", "HTTP_CF_CONNECTING_IP": "198.51.100.9"}
    assert ClientIPMiddleware(None, ["127.0.0.1/32"]).resolve(environ) == "172.64.0.10"


def test_wsgi_environ_is_updated_for_gunicorn_access_logs():
    environ = {"REMOTE_ADDR": "127.0.0.1", "HTTP_X_FORWARDED_FOR": "203.0.113.12"}
    observed = {}

    def downstream(environ, start_response):
        observed.update(environ)
        return []

    ClientIPMiddleware(downstream, ["127.0.0.1/32"])(environ, None)
    assert environ["REMOTE_ADDR"] == observed["REMOTE_ADDR"] == "203.0.113.12"
