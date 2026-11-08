"""Seeds (cloudforge/seed.py): the data every installation needs, safe to apply on every start."""

from cloudforge.migrate import migrate
from cloudforge.seed import seed


def test_seeds_are_safe_to_apply_again(owner):
    migrate(owner)

    assert seed(owner) == ["0001_default_organization"]
    assert seed(owner) == ["0001_default_organization"]
    assert owner.execute("SELECT slug FROM organization").fetchall() == [("default",)]
