# syntax=docker/dockerfile:1.7

# BuildKit fornece TARGETOS/TARGETARCH para o artefato de destino enquanto o
# compilador roda na plataforma nativa do builder.
FROM --platform=$BUILDPLATFORM golang:1.26.6 AS build

# Re-declare os argumentos automáticos do BuildKit dentro do estágio para que
# cada plataforma compile seu próprio artefato, sem um default que mascare o
# alvo solicitado.
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

WORKDIR /src

ENV CGO_ENABLED=0 \
    GOTOOLCHAIN=local

COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal

# Os diretórios são preparados com o mesmo UID/GID do processo final para
# permitir um volume de saída gravável sem adicionar utilitários à imagem.
RUN mkdir -p /runtime/source /runtime/output \
    && chown 65532:65532 /runtime/source /runtime/output

RUN GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" CGO_ENABLED=0 \
    go build \
      -trimpath \
      -buildvcs=false \
      -ldflags="-s -w -X github.com/pedrogpaulino/manu/internal/buildinfo.version=${VERSION} -X github.com/pedrogpaulino/manu/internal/buildinfo.commit=${COMMIT} -X github.com/pedrogpaulino/manu/internal/buildinfo.buildDate=${BUILD_DATE}" \
      -o /out/manu \
      ./cmd/manu

FROM scratch

COPY --from=build --chown=65532:65532 /out/manu /manu
COPY --from=build --chown=65532:65532 /runtime/source /source
COPY --from=build --chown=65532:65532 /runtime/output /output

USER 65532:65532
WORKDIR /output

# Em runtime, monte /source como somente leitura (:ro) e /output como volume
# gravável separado.
VOLUME ["/source", "/output"]

ENTRYPOINT ["/manu"]
