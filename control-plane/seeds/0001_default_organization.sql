-- Seed data (ADR-0014): applied after the migrations on every start, so it must be safe to run again.
-- Until logins exist (M4), one organization owns everything (docs/api/dashboard.md, ADR-0015).
INSERT INTO organization (slug, name) VALUES ('default', 'Default') ON CONFLICT (slug) DO NOTHING;
