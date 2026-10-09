# syntax=docker/dockerfile:1
#
# Everything the lab runs.
#
#   znnd   the node
#   lab    the lab console: labd, the web page, znn-cli and the scenarios
#   suite  the go-zenon tree with a toolchain, for `go test`
#   dart   the patched Dart SDK and CLI with a toolchain, for `dart test`
#
# Each of the three source stages (gozenon, dartsdk, dartcli) is replaced by a
# build context of the same name when one is given. compose.yml gives `gozenon`
# as the go-zenon tree this directory sits in, so the node and the scenarios
# are built from the branch that is checked out.
#
# Without that context, and always for the Dart SDK and CLI, the upstream tree
# is fetched at the commit the patches were cut against, the patches are
# applied, and the result is checked against the tree hash of the branch they
# were exported from. A patch set that has drifted from its branch fails the
# build here rather than producing a node that is almost the contract. For
# go-zenon that path is release/0.0.9 + patches/ (compose.release.yml).

ARG GO_ZENON_REPO=https://github.com/zenon-network/go-zenon.git
ARG GO_ZENON_BASE=a681890f0320921a8f3aec1a28fe3406674edd0b
ARG GO_ZENON_TREE=c6f2f50b2319edee802f1b4cb31b1895e41f3fa5
# The directory the go-zenon patches are read from. It may hold none, when the
# base is itself the branch.
ARG GO_ZENON_PATCHES=patches
ARG DART_SDK_REPO=https://github.com/zenon-network/znn_sdk_dart.git
ARG DART_SDK_BASE=b21f0f866f4a0d5ae15d86c76b7ae414ca0b973a
ARG DART_SDK_TREE=3ee2e33c82f9967f4223773ed931b56ecfd4b38d
ARG DART_CLI_REPO=https://github.com/zenon-network/znn_cli_dart.git
ARG DART_CLI_BASE=30c134005e0b19c4239fb4d1bf3da9b372a2066c
ARG DART_CLI_TREE=101b7f83de47dbcb3b3e56daa01c2e6ace493373

# ---- sources ---------------------------------------------------------------
FROM alpine:3.22 AS patcher
RUN apk add --no-cache git
COPY docker/fetch-and-patch.sh /usr/local/bin/fetch-and-patch
RUN chmod +x /usr/local/bin/fetch-and-patch

FROM patcher AS gozenon-patched
ARG GO_ZENON_REPO GO_ZENON_BASE GO_ZENON_TREE GO_ZENON_PATCHES
COPY ${GO_ZENON_PATCHES} /patches
RUN fetch-and-patch "$GO_ZENON_REPO" "$GO_ZENON_BASE" "$GO_ZENON_TREE" /patches /src

FROM patcher AS dartsdk-patched
ARG DART_SDK_REPO DART_SDK_BASE DART_SDK_TREE
COPY dart/sdk /patches
RUN fetch-and-patch "$DART_SDK_REPO" "$DART_SDK_BASE" "$DART_SDK_TREE" /patches /src

FROM patcher AS dartcli-patched
ARG DART_CLI_REPO DART_CLI_BASE DART_CLI_TREE
COPY dart/cli /patches
RUN fetch-and-patch "$DART_CLI_REPO" "$DART_CLI_BASE" "$DART_CLI_TREE" /patches /src

# A stage named like a build context is replaced by that context when one is
# given, so these three hold the tree at their root, the way a checkout does.
FROM scratch AS gozenon
COPY --from=gozenon-patched /src /
FROM scratch AS dartsdk
COPY --from=dartsdk-patched /src /
FROM scratch AS dartcli
COPY --from=dartcli-patched /src /

# ---- znnd ------------------------------------------------------------------
# The branch's own Dockerfile, with the source coming from the stage above.
FROM golang:1.20-alpine AS znnd-build
RUN apk add --no-cache make gcc musl-dev git
WORKDIR /app
COPY --from=gozenon /go.mod /go.sum ./
RUN go mod download
COPY --from=gozenon / .
# No .git in a patched tree, and a worktree's .git is a pointer to a path that
# does not exist in here; either way there is no revision to stamp.
RUN go build -buildvcs=false -o /out/znnd ./cmd/znnd

FROM alpine:3.22 AS znnd
RUN apk add --no-cache ca-certificates
WORKDIR /root
RUN mkdir -p /root/.znn
COPY --from=znnd-build /out/znnd /usr/local/bin/znnd
# This repository's devnet, which compose mounts over it again. It used to be
# the branch's own docker/devnet, the same tree on another subnet; the branch
# cut for the pull request carries no devnet, and a build from it stopped here.
COPY devnet/assets /devnet
COPY devnet/assets/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 35995 35997 35998
ENTRYPOINT ["/entrypoint.sh"]

# ---- go test, and the NoM-to-BTC scenario ----------------------------------
# The scenario's go.mod replaces go-zenon with ../../.., the tree this
# directory sits in, so the two are laid out here as they are in a checkout.
FROM golang:1.27 AS suite
WORKDIR /src
COPY --from=gozenon /go.mod /go.sum ./
RUN go mod download
COPY --from=gozenon / .
ENV GOFLAGS=-buildvcs=false
CMD ["go", "test", "-count=1", "./vm/embedded/...", "./rpc/api/embedded/..."]

FROM suite AS scenario-build
WORKDIR /src/ptlc-lab/scenarios/nom-to-btc
COPY scenarios/nom-to-btc/go.mod scenarios/nom-to-btc/go.sum ./
RUN go mod download
COPY scenarios/nom-to-btc .
COPY test/vectors /src/ptlc-lab/test/vectors
# Static, so the binary does not care which libc the lab image carries.
RUN CGO_ENABLED=1 go build -tags netgo,osusergo \
      -ldflags '-linkmode external -extldflags "-static"' -o /out/nom-to-btc .
# flows is the scenarios page's back end: it signs every step of a named
# scenario itself. Its catalogue test runs here, because the page draws from
# that catalogue and a step it could not draw should stop the build.
RUN go test -count=1 ./cmd/flows/ \
 && CGO_ENABLED=1 go build -tags netgo,osusergo \
      -ldflags '-linkmode external -extldflags "-static"' -o /out/flows ./cmd/flows
# keyswap is the key swap's own client. Its tests run both parties against
# go-zenon's mock chain, with the contract of the tree above, through every
# restart and every refusal: a client that loses a swap there is not shipped.
RUN go test -count=1 ./keyswap/  && CGO_ENABLED=1 go build -tags netgo,osusergo       -ldflags '-linkmode external -extldflags "-static"' -o /out/keyswap ./cmd/keyswap

# ---- znn-cli ---------------------------------------------------------------
# The CLI's pubspec overrides the SDK with ../znn_sdk_dart-ptlc.
FROM dart:3.13 AS dart
WORKDIR /src/znn_sdk_dart-ptlc
COPY --from=dartsdk / .
RUN dart pub get
WORKDIR /src/znn_cli_dart-ptlc
COPY --from=dartcli / .
RUN dart pub get
COPY test/vectors /src/zenon-ptlc/test/vectors
WORKDIR /src/znn_sdk_dart-ptlc
CMD ["dart", "test"]

FROM dart AS cli-build
WORKDIR /src/znn_cli_dart-ptlc
RUN mkdir -p /out && dart compile exe znn-cli.dart -o /out/znn-cli \
 && cp Resources/*.so /out/

# ---- labd and the page -----------------------------------------------------
FROM golang:1.27 AS labd-build
WORKDIR /src
COPY lab/server .
# Its tests are the rules that hold once the lab has a public name: what a
# request may make the container do. An image whose labd fails them is not
# built.
RUN go vet ./... && go test -count=1 ./... && CGO_ENABLED=0 go build -trimpath -o /out/labd .

FROM node:24-alpine AS web-build
WORKDIR /src/lab/web
COPY lab/web/package.json lab/web/package-lock.json ./
RUN npm ci
COPY lab/web .
# The page's own tests read the vectors the Go and Dart suites share.
COPY test/vectors /src/test/vectors
RUN npm test && npm run build

# ---- the lab image ---------------------------------------------------------
FROM debian:trixie-slim AS lab
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY --from=cli-build /out/ /opt/znn-cli/
COPY --from=scenario-build /out/nom-to-btc /usr/local/bin/nom-to-btc
COPY --from=scenario-build /out/flows /usr/local/bin/flows
COPY --from=scenario-build /out/keyswap /usr/local/bin/keyswap
COPY --from=labd-build /out/labd /usr/local/bin/labd
COPY --from=web-build /src/lab/web/dist /srv/web
COPY scenarios/nom-to-nom/run.sh /opt/scenarios/nom-to-nom.sh
COPY docker/lab-entrypoint.sh /usr/local/bin/lab-entrypoint
RUN chmod +x /usr/local/bin/lab-entrypoint /opt/scenarios/nom-to-nom.sh \
 && ln -s /opt/znn-cli/znn-cli /usr/local/bin/znn-cli
ARG GO_ZENON_BASE DART_SDK_BASE DART_CLI_BASE
ENV PTLC_GO_ZENON_BASE=$GO_ZENON_BASE \
    PTLC_DART_SDK_BASE=$DART_SDK_BASE \
    PTLC_DART_CLI_BASE=$DART_CLI_BASE \
    LAB_WEB_ROOT=/srv/web \
    LAB_LISTEN=0.0.0.0:8470
EXPOSE 8470
ENTRYPOINT ["lab-entrypoint"]
CMD ["labd"]
