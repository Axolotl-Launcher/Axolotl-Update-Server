from datetime import timedelta
from urllib.parse import urlencode

from app.extensions import db
from app.models import UsageEvent, utcnow


def test_admin_stats_counts_api_channels_and_download_bytes(client, app):
    now = utcnow()
    with app.app_context():
        db.session.add_all([
            UsageEvent(occurred_at=now - timedelta(hours=2), path="/latest", version="1.9.3", channel="release", event_type="api", status_code=200),
            UsageEvent(occurred_at=now - timedelta(hours=1), path="/api/downloads/latest", version="1.9.4", channel="beta", event_type="api", status_code=200),
            UsageEvent(occurred_at=now - timedelta(minutes=30), path="/dist/1.9.4/a.zip", version="1.9.4", platform="windows-x86_64", channel="release", event_type="download", status_code=200, bytes_sent=123),
            UsageEvent(occurred_at=now - timedelta(minutes=20), path="/dist/1.9.4/b.zip", version="1.9.4", platform="linux-x86_64", channel="beta", event_type="download", status_code=206, bytes_sent=77),
        ])
        db.session.commit()
    start = (now - timedelta(days=1)).isoformat()
    end = (now + timedelta(minutes=1)).isoformat()
    response = client.get("/api/admin/stats?" + urlencode({"start": start, "end": end}), headers={"Authorization": "Bearer admin"})
    assert response.status_code == 200
    assert response.json["api_calls"] == {"total": 2, "groups": [{"version": "1.9.3", "channel": "release", "count": 1}, {"version": "1.9.4", "channel": "beta", "count": 1}]}
    assert response.json["downloads"]["total_count"] == 2
    assert response.json["downloads"]["total_bytes"] == 200
    assert response.json["downloads"]["groups"] == [{"version": "1.9.4", "platform": "linux-x86_64", "channel": "beta", "count": 1, "bytes": 77}, {"version": "1.9.4", "platform": "windows-x86_64", "channel": "release", "count": 1, "bytes": 123}]


def test_stats_requires_admin_and_limits_range(client):
    assert client.get("/api/admin/stats").status_code == 401
    response = client.get("/api/admin/stats?start=2026-01-01T00:00:00Z&end=2026-02-01T00:00:01Z", headers={"Authorization": "Bearer admin"})
    assert response.status_code == 400
    assert response.json["error"]["code"] == "invalid_stats_range"


def test_request_hook_records_latest_version_and_download_dimensions(client, app):
    client.get("/latest?channel=beta", headers={"X-Axolotl-Version": "1.9.3"})
    response = client.get("/api/admin/stats", headers={"Authorization": "Bearer admin"})
    assert response.status_code == 200
    assert {tuple(item[key] for key in ("version", "channel")) for item in response.json["api_calls"]["groups"]} >= {("1.9.3", "beta")}
