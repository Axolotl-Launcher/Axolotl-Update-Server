import os
import tempfile
from collections import OrderedDict
from pathlib import Path
from threading import RLock
from time import monotonic

from flask import current_app, has_app_context
from sqlalchemy import event
from sqlalchemy.orm import Session

from ..models import Artifact, ChannelPointer, Version


class LatestCache:
    """Bounded per-worker cache with filesystem invalidation across workers."""

    def __init__(self, revision_path: Path):
        self.revision_path = revision_path
        self.entries: OrderedDict = OrderedDict()
        self.lock = RLock()

    def revision(self):
        try:
            return self.revision_path.read_bytes()
        except FileNotFoundError:
            return None

    def get_or_build(self, key, ttl, build):
        with self.lock:
            revision = self.revision()
            cached = self.entries.get(key)
            if ttl > 0 and cached and cached[0] > monotonic() and cached[1] == revision:
                self.entries.move_to_end(key)
                return cached[2]
            value = build()
            if ttl > 0:
                self.entries[key] = (monotonic() + ttl, revision, value)
                self.entries.move_to_end(key)
                while len(self.entries) > 256:
                    self.entries.popitem(last=False)
            return value

    def invalidate(self):
        with self.lock:
            self.entries.clear()
            self.revision_path.parent.mkdir(parents=True, exist_ok=True)
            temporary = None
            try:
                with tempfile.NamedTemporaryFile(dir=self.revision_path.parent, delete=False) as output:
                    temporary = Path(output.name)
                    output.write(os.urandom(16))
                temporary.replace(self.revision_path)
            finally:
                if temporary:
                    temporary.unlink(missing_ok=True)


@event.listens_for(Session, "before_flush")
def track_update_changes(session, flush_context, instances):
    if any(isinstance(item, (Version, Artifact, ChannelPointer)) for item in (*session.new, *session.dirty, *session.deleted)):
        session.info["invalidate_latest"] = True


@event.listens_for(Session, "after_commit")
def invalidate_after_commit(session):
    changed = session.info.pop("invalidate_latest", False)
    if changed and has_app_context():
        cache = current_app.extensions.get("latest_cache")
        if cache:
            try:
                cache.invalidate()
            except OSError:
                current_app.logger.exception("Could not invalidate latest cache across workers; TTL will expire it")


@event.listens_for(Session, "after_rollback")
def discard_update_changes(session):
    session.info.pop("invalidate_latest", None)
