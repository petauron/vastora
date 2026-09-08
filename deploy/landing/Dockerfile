# syntax=docker/dockerfile:1
# Build against the oldest supported glibc (Ubuntu 22.04). Cross-compilers
# avoid emulating builds or installing compilers on the landing machine.
FROM --platform=$BUILDPLATFORM ubuntu:22.04 AS build
ARG TARGETARCH
RUN apt-get update -qq && apt-get install -y --no-install-recommends \
    ca-certificates curl make gcc-aarch64-linux-gnu gcc-x86-64-linux-gnu \
    libc6-dev-arm64-cross libc6-dev-amd64-cross \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /build
COPY scripts/build-landing-dante.sh /build/build-landing-dante.sh
RUN sh /build/build-landing-dante.sh "$TARGETARCH" /out

FROM scratch AS artifact
COPY --from=build /out/ /
