import json
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
def test_real_client_ip_in_audit_and_log(client, app, caplog, peer, headers, expected):
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
    record = next(record for record in caplog.records if record.getMessage().startswith("request "))
    message = record.getMessage()
    logged = json.loads(message.removeprefix("request "))
    assert logged["client_ip"] == expected and logged["proxy_peer"] == peer
    assert logged["request_id"] == response.headers["X-Request-ID"]
    assert logged["status"] == 200 and logged["method"] == "POST"
    assert "not-logged" not in message and "Bearer" not in message


def test_rate_limit_is_per_resolved_client_and_errors_are_logged(client, app, caplog):
    app.config["RATE_LIMIT_PER_MINUTE"] = 1
    assert client.get("/api/health", headers={"X-Forwarded-For": "203.0.113.1"}).status_code == 200
    assert client.get("/api/health", headers={"X-Forwarded-For": "203.0.113.2"}).status_code == 200
    with caplog.at_level(logging.INFO):
        response = client.get("/api/health", headers={"X-Forwarded-For": "203.0.113.1"})
    assert response.status_code == 429 and response.headers["X-Request-ID"]
    assert any('"status": 429' in record.getMessage() and '"client_ip": "203.0.113.1"' in record.getMessage() for record in caplog.records)


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
