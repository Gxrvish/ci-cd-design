-- Until logins exist (M4), one seeded organization owns everything (docs/api/dashboard.md, ADR-0015).
INSERT INTO organization (slug, name) VALUES ('default', 'Default');
