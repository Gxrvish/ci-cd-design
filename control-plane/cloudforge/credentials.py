"""Bearer secrets: shown once, stored only as SHA-256 (ADR-0007, threat model #10).

They are 256 random bits, so a fast hash is enough; slow hashing is for passwords (ADR-0015).
"""

import hashlib
import secrets


def new_secret() -> tuple[str, bytes]:
    """A new secret for the caller, and the digest to store."""
    secret = secrets.token_urlsafe(32)
    return secret, digest(secret)


def digest(secret: str) -> bytes:
    return hashlib.sha256(secret.encode()).digest()
