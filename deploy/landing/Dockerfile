# syntax=docker/dockerfile:1
# Build against the oldest supported glibc (Ubuntu 22.04). Cross-compilers
# avoid emulating builds or installing compilers on the landing machine.
FROM --platform=$BUILDPLATFORM ubuntu:22.04 AS build
ARG TARGETARCH
COPY scripts/install-landing-build-deps.sh /install-build-deps.sh
RUN sh /install-build-deps.sh
WORKDIR /build
COPY scripts/build-landing-dante.sh /build/build-landing-dante.sh
RUN sh /build/build-landing-dante.sh "$TARGETARCH" /out

FROM scratch AS artifact
COPY --from=build /out/ /
