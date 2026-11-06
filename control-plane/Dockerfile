# One image for migrate, api and (from M2) worker. Dependencies first, code last, for the build cache.
FROM python:3.14.10-slim AS runtime
RUN useradd --system --no-create-home cloudforge
WORKDIR /app
COPY pyproject.toml .
RUN pip install --no-cache-dir --root-user-action=ignore --group runtime
COPY migrations migrations
COPY cloudforge cloudforge
USER cloudforge
CMD ["uvicorn", "cloudforge.api:app", "--host", "0.0.0.0", "--port", "8000"]

# `make test`: the same image plus the test tools and the tests.
FROM runtime AS test
USER root
RUN pip install --no-cache-dir --root-user-action=ignore --group test
COPY tests tests
USER cloudforge
CMD ["sh", "-c", "ruff check --no-cache . && ruff format --no-cache --check . && pytest"]
