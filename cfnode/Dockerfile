# The image of the node program: the cfnode binary, built on the host by `make cfnode`, plus git for checkouts.
# It runs as uid 61000, like buildkitd, to reach buildkitd's socket on their shared volume (ADR-0009).
FROM alpine:3.23.8
RUN apk add --no-cache git
COPY bin/cfnode /usr/local/bin/cfnode
USER 61000:61000
ENTRYPOINT ["cfnode"]
