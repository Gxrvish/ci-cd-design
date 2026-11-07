# Rootless buildkitd as uid 61000, above this host's UID_MAX (60000), so no person has that uid (ADR-0009).
# A build that escapes its sandbox lands as that uid, not in your files. The runner runs as the same uid (3c).
FROM moby/buildkit:v0.34.2-rootless
USER root
RUN sed -i 's/^user:x:1000:1000:/user:x:61000:61000:/' /etc/passwd \
 && sed -i 's/^user:x:1000:/user:x:61000:/' /etc/group \
 && mkdir -p /run/user/61000 \
 && chown -R 61000:61000 /home/user /run/user/61000
COPY buildkitd.toml /etc/buildkit/buildkitd.toml
USER 61000:61000
ENV XDG_RUNTIME_DIR=/run/user/61000 BUILDKIT_HOST=unix:///run/user/61000/buildkit/buildkitd.sock
CMD ["--config", "/etc/buildkit/buildkitd.toml"]
