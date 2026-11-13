"""op:// reference rendering through (fake) 1Password Connect, and secret masking."""
import pytest

from conftest import rc

ITEM_ID = "abcdefghijklmnopqrstuvwxyz"  # 26 chars


def fake_connect(calls):
    items = {
        "i1": {"id": "i1", "sections": [{"id": "s1", "label": "db"}],
               "fields": [{"id": "f1", "label": "password", "value": "hunter2-secret"},
                          {"id": "f2", "label": "password", "section": {"id": "s1"}, "value": "db-pass-value"},
                          {"id": "f3", "label": "empty"}]},
        ITEM_ID: {"id": ITEM_ID, "fields": [{"id": "token", "label": "token", "value": "tok-123456"}]},
    }

    def _connect(path):
        calls.append(path)
        if path.startswith("/v1/vaults?"):
            return [{"id": "v1"}] if "Infrastructure" in path else []
        if "/items?filter=" in path:
            return [{"id": "i1"}] if "app1" in path else []
        return items[path.rsplit("/", 1)[1]]
    return _connect


@pytest.fixture
def rctx(ctx, monkeypatch):
    ctx.calls = []
    monkeypatch.setattr(ctx, "_connect", fake_connect(ctx.calls))
    return ctx


def test_render_title_section_and_item_id(rctx):
    out = rctx.render_obj({"a": "pw={{ op://Infrastructure/app1/password }}",
                           "b": ["{{op://Infrastructure/app1/db/password}}", 5],
                           "c": f"{{{{ op://Infrastructure/{ITEM_ID}/token }}}}", "d": True})
    assert out == {"a": "pw=hunter2-secret", "b": ["db-pass-value", 5], "c": "tok-123456", "d": True}
    # item id refs are fetched directly, without a title search
    assert not any(f"title%20eq%20%22{ITEM_ID}" in c for c in rctx.calls)


def test_resolve_is_cached(rctx):
    rctx.render_str("{{ op://Infrastructure/app1/password }}")
    n = len(rctx.calls)
    rctx.render_str("{{ op://Infrastructure/app1/password }} {{ op://Infrastructure/app1/password }}")
    assert len(rctx.calls) == n


@pytest.mark.parametrize("ref,exc", [
    ("op://Nope/app1/password", KeyError),
    ("op://Infrastructure/missing/password", KeyError),
    ("op://Infrastructure/app1/nofield", KeyError),
    ("op://Infrastructure/app1/empty", KeyError),
    ("op://Infrastructure/app1", ValueError),
])
def test_resolve_errors(rctx, ref, exc):
    with pytest.raises(exc):
        rctx.resolve(ref)


def test_mask(rctx):
    rctx.render_str("{{ op://Infrastructure/app1/password }}")
    rctx.secrets["op://x/y/short"] = "abc"  # < 4 chars: not masked (would mangle normal text)
    assert rctx.mask("err: hunter2-secret abc") == "err: <op://Infrastructure/app1/password> abc"


def test_rendering_without_refs_does_not_call_connect(rctx):
    assert rctx.render_obj({"x": "{{ not a ref }}"}) == {"x": "{{ not a ref }}"}
    assert rctx.calls == []
