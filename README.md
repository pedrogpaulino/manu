# Manu

O Manu transforma fontes técnicas e documentais em uma base de conhecimento
viva. O **Knowledge Engine** é o núcleo; catálogo, documentação/wiki, grafo,
busca, chat, onboarding e análise de impacto são experiências derivadas dessa
base.

## Estado atual

O primeiro corte executável é o **Manu Agent** em Go. Neste incremento, a CLI é
o primeiro modo de execução do Agent: ela recebe uma configuração explícita,
lê um recorte local autorizado, executa uma análise determinística e grava o
resultado em um destino separado. Isso não é um daemon e não inclui IA local,
banco de dados principal, API HTTP ou serviço de cloud.

O microcorte oferece descoberta, hashing, inventário genérico, abertura segura
de CARs e extração estrutural mínima de Java/XML/texto. Seus resultados
preservam evidências, cobertura, lacunas e falhas parciais; não representam
compreensão semântica completa nem `Observed Execution`.

## Pré-requisitos

- Linux para o binário e a imagem iniciais.
- Go 1.25 ou mais recente. O módulo declara `go 1.25` e usa `toolchain go1.26.6`;
  a verificação desta mudança foi feita com `go version go1.26.6 linux/amd64`.
- Docker com Buildx somente para a verificação da imagem. A execução local não
  exige Docker, banco, modelo, credencial ou acesso de cloud.

## Construção e comandos da CLI

Os comandos abaixo usam apenas a fixture pequena e não sensível do repositório.
O diretório de fonte é entrada; os diretórios de saída ficam fora dele.

```bash
MANU_BIN="${MANU_BIN:-/tmp/manu}"
SOURCE_DIR="${SOURCE_DIR:-$PWD/testdata/analyzers}"
ANALYZE_OUTPUT_DIR="$(mktemp -d /tmp/manu-analyze.XXXXXX)"
BENCHMARK_OUTPUT_DIR="$(mktemp -d /tmp/manu-benchmark.XXXXXX)"

go build -trimpath -buildvcs=false -o "$MANU_BIN" ./cmd/manu
"$MANU_BIN" version
"$MANU_BIN" analyze \
  --root "$SOURCE_DIR" \
  --output "$ANALYZE_OUTPUT_DIR" \
  --source-id fixture-analyzers
"$MANU_BIN" inspect --input "$ANALYZE_OUTPUT_DIR"
"$MANU_BIN" benchmark \
  --root "$SOURCE_DIR" \
  --output "$BENCHMARK_OUTPUT_DIR" \
  --source-id fixture-analyzers
```

Na fixture, `analyze`, `inspect` e `benchmark` retornam `3` porque o resultado
é válido, mas parcial: o extrator Java é léxico e o analisador WSO2 não
reconstrói semântica dinâmica ou de runtime. O código `0` indica sucesso sem
lacunas ou falhas parciais; `1` é falha técnica; `2` é uso ou configuração
inválida; `3` é resultado parcial utilizável.

Para saída automatizável, acrescente `--json` a `version`, `analyze` ou
`benchmark`; `inspect --json` lê o resultado estruturado `v1alpha1` já gravado.

## Verificações locais

```bash
gofmt -d $(rg --files -g '*.go')
go vet ./...
go test ./... -count=1
go mod verify

GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build \
  -trimpath -buildvcs=false -o /tmp/manu-static-amd64 ./cmd/manu
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build \
  -trimpath -buildvcs=false -o /tmp/manu-static-arm64 ./cmd/manu
file /tmp/manu-static-amd64 /tmp/manu-static-arm64
```

`go test -race ./...` deve ser executado quando CGO e um compilador C estiverem
disponíveis. Na verificação desta mudança `CGO_ENABLED=0` e `gcc` estavam
indisponíveis, portanto o detector de corrida não foi executado. `govulncheck`
também é uma ferramenta opcional: não estava instalada e não foi instalada nem
executada, sem acesso de rede adicional.

## Imagem Linux

O Dockerfile usa build multi-stage com Go 1.26.6, `CGO_ENABLED=0` e runtime
`scratch`. As imagens são compiladas para `linux/amd64` e `linux/arm64`; o
artefato arm64 é verificado por `file` antes de qualquer execução. O processo
final usa UID/GID `65532:65532`, não possui shell e não precisa de credenciais,
IA, banco ou cloud.

```bash
docker buildx build --no-cache --platform linux/amd64 \
  --load --tag manu:local-amd64 .
docker buildx build --no-cache --platform linux/arm64 \
  --load --tag manu:local-arm64 .

SOURCE_DIR="${SOURCE_DIR:-$PWD/testdata/analyzers}"
OUTPUT_VOLUME="manu-output-$(date +%s)"
docker volume create "$OUTPUT_VOLUME"
docker run --rm --read-only --network none --cap-drop=ALL \
  --security-opt=no-new-privileges:true \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=16m \
  --mount "type=bind,src=$SOURCE_DIR,dst=/source,readonly" \
  --mount "type=volume,src=$OUTPUT_VOLUME,dst=/output" \
  manu:local-amd64 version
docker run --rm --read-only --network none --cap-drop=ALL \
  --security-opt=no-new-privileges:true \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=16m \
  --mount "type=bind,src=$SOURCE_DIR,dst=/source,readonly" \
  --mount "type=volume,src=$OUTPUT_VOLUME,dst=/output" \
  manu:local-amd64 analyze --root /source --output /output \
  --source-id fixture-analyzers
docker run --rm --read-only --network none --cap-drop=ALL \
  --security-opt=no-new-privileges:true \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=16m \
  --mount "type=volume,src=$OUTPUT_VOLUME,dst=/output,readonly" \
  manu:local-amd64 inspect --input /output
docker volume rm "$OUTPUT_VOLUME"
```

Na execução em contêiner, `/source` é somente leitura e `/output` é a única
montagem gravável. O runtime não executa arquivos da fonte e não usa o
diretório da fonte para estado temporário. O volume nomeado é removível ao fim
da verificação; uma execução de produção deve preservar o destino de saída
conforme sua política operacional.

## Limites do microcorte

- A análise é local, limitada por quantidade/tamanho de arquivos, bytes,
  membros e expansão de arquivos compactados, concorrência e duração.
- O fallback genérico mantém inventário, identidade, hash e metadados de texto.
  Java é analisado por extração lexical conservadora; WSO2 cobre inventário de
  membros CAR e referências XML literais. Semântica profunda de Java/Quarkus,
  WSO2, Python/Frappe, execução de negócio e telemetria estão fora do corte.
- O estado incremental é reconstruível e fica na saída. A repetição reutiliza
  hashes compatíveis; a atualização é um overlay efêmero e só invalida o
  artefato alterado e dependentes diretos conhecidos.
- O benchmark mede primeira análise, repetição e atualização localizada, com
  duração, bytes, memória disponível, volume, concorrência, reutilização e
  falhas. É uma linha de base local, não um SLA nem uma medida de capacidade
  comercial. Corpus externo só deve ser usado explicitamente, com revisão/hash
  fixos e montagem somente leitura.

## Fontes de verdade

- [`PRODUCT.md`](PRODUCT.md) — problema, públicos, valor, experiências, MVP e hipóteses.
- [`DOMAIN.md`](DOMAIN.md) — linguagem ubíqua e modelo conceitual do conhecimento e da colaboração.
- [`ARCHITECTURE.md`](ARCHITECTURE.md) — arquitetura conceitual, fluxos, restrições, segurança e implantação.
- [`AGENTS.md`](AGENTS.md) — orientação operacional para colaboradores e agentes.
- [`docs/decisions/README.md`](docs/decisions/README.md) — processo e template para decisões arquiteturais aceitas.
- [`docs/decisions/0001-contrato-universal-de-compreensao.md`](docs/decisions/0001-contrato-universal-de-compreensao.md) — contrato universal de compreensão.
- [`docs/decisions/0002-fundacao-go-first.md`](docs/decisions/0002-fundacao-go-first.md) — decisão Go-first e seus trade-offs.
- [`docs/evaluation/knowledge-engine-go-first-baseline.md`](docs/evaluation/knowledge-engine-go-first-baseline.md) — linha de base do microcorte e limitações das métricas.
- [`docs/evaluation/first-vertical-slice-corpus.md`](docs/evaluation/first-vertical-slice-corpus.md) — manifesto do corpus e dos recortes.
- [`docs/evaluation/first-vertical-slice-evaluation.md`](docs/evaluation/first-vertical-slice-evaluation.md) — protocolo de avaliação, perguntas e métricas.
- [`openspec/changes/benchmark-select-knowledge-engine-stack/specs/knowledge-engine-runtime/spec.md`](openspec/changes/benchmark-select-knowledge-engine-stack/specs/knowledge-engine-runtime/spec.md) — requisitos de runtime desta mudança.
- [`openspec/`](openspec/) — mudanças, especificações e tarefas rastreáveis.

Comece por [`AGENTS.md`](AGENTS.md) para a ordem de leitura e as regras de
trabalho.
