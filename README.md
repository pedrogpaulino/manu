# Manu

O Manu transforma fontes técnicas e documentais em uma base de conhecimento
viva. O **Knowledge Engine** é o núcleo; catálogo, documentação/wiki, grafo,
busca, chat, onboarding e análise de impacto são experiências derivadas dessa
base.

## Estado atual

O repositório contém três limites operacionais do primeiro corte, todos no
módulo Go:

- o **Manu Agent**, executado pela CLI, lê somente um recorte local autorizado,
  faz análise determinística e grava o resultado em um destino separado;
- a **plataforma local**, iniciada por `manu serve`, expõe a API HTTP versionada,
  usa PostgreSQL como fonte de verdade e mantém projeções textual, relacional e
  vetorial reconstruíveis. A célula Docker Compose fornece PostgreSQL/pgvector,
  migração e API.
- o **adaptador MCP local**, iniciado por `manu mcp` com
  `MANU_MCP_ENABLED=true`, expõe somente contexto autorizado por `stdio`; sua
  configuração, ferramentas e limites estão em [Servidor MCP local](docs/mcp.md).

O fluxo de produto é:

```text
Agent → Analysis Bundle → API HTTP → PostgreSQL/projeções → consulta/evidências
                                                     ↘ AI Gateway opcional
```

O Agent não abre conexão com o banco. O bundle é enviado em multipart, em
fluxo, sem transferir o diretório da fonte ou arquivos inteiros por padrão.
Enquanto o modo servidor não tiver autenticação, ele permanece vinculado a uma
única `Organization` e a endereço de loopback.

O `manu analyze` mantém o formato `legacy` (`v1alpha1`) como padrão para
compatibilidade. Para produzir o bundle que a plataforma ingere, use
`--output-mode bundle` e informe explicitamente `--organization-id`; esse modo
escreve o `Analysis Bundle` estendido (`manifest.json` e sequências canônicas)
sem transportar a raiz local da fonte. O caminho Agent → bundle → API →
PostgreSQL/projeções foi verificado na célula local; os detalhes, resultados e
limitações estão no [registro de verificação 10.3](docs/verification/10-3-local-cell.md).
O bundle continua sendo um envelope de dados não confiáveis: validação,
política e limites ocorrem antes da persistência ou de qualquer chamada
externa.

O microcorte oferece descoberta, hashing, inventário genérico, abertura segura
de CARs e extração estrutural mínima de Java/XML/texto. Seus resultados
preservam evidências, cobertura, lacunas e falhas parciais; não representam
compreensão semântica completa nem `Observed Execution`.

## Pré-requisitos

- Linux para o binário e a imagem iniciais.
- Go 1.25 ou mais recente. O módulo declara `go 1.25` e usa `toolchain go1.26.6`;
  a verificação desta mudança foi feita com `go version go1.26.6 linux/amd64`.
- Docker Compose e Buildx para a célula local e a verificação da imagem.
- PostgreSQL/pgvector somente quando a plataforma local for usada; o Agent
  determinístico continua funcionando sem banco, modelo, credencial ou cloud.

## Construção e comandos da CLI

Os comandos abaixo usam apenas a fixture pequena e não sensível do repositório.
O diretório de fonte é entrada; os diretórios de saída ficam fora dele.

```bash
MANU_BIN="${MANU_BIN:-/tmp/manu}"
SOURCE_DIR="${SOURCE_DIR:-$PWD/testdata/analyzers}"
ANALYZE_OUTPUT_DIR="$(mktemp -d /tmp/manu-analyze.XXXXXX)"
BENCHMARK_OUTPUT_DIR="$(mktemp -d /tmp/manu-benchmark.XXXXXX)"
BUNDLE_OUTPUT_DIR="$(mktemp -d /tmp/manu-bundle.XXXXXX)"

go build -trimpath -buildvcs=false -o "$MANU_BIN" ./cmd/manu
"$MANU_BIN" version
"$MANU_BIN" analyze \
  --root "$SOURCE_DIR" \
  --output "$ANALYZE_OUTPUT_DIR" \
  --source-id fixture-analyzers
"$MANU_BIN" inspect --input "$ANALYZE_OUTPUT_DIR"
"$MANU_BIN" analyze \
  --root "$SOURCE_DIR" \
  --output "$BUNDLE_OUTPUT_DIR" \
  --output-mode bundle \
  --organization-id local \
  --source-id fixture-analyzers
"$MANU_BIN" benchmark \
  --root "$SOURCE_DIR" \
  --output "$BENCHMARK_OUTPUT_DIR" \
  --source-id fixture-analyzers
"$MANU_BIN" eval --json
```

Na fixture, `analyze`, `inspect`, `benchmark` e `eval` podem retornar `3` porque
o resultado é válido, mas parcial: o extrator Java é léxico, o analisador WSO2
não reconstrói semântica dinâmica ou de runtime e a avaliação preserva suas
abstenções/lacunas. O código `0` indica sucesso sem lacunas ou falhas parciais;
`1` é falha técnica; `2` é uso ou configuração inválida; `3` é resultado
parcial utilizável.

Para saída automatizável, acrescente `--json` a `version`, `analyze` ou
`benchmark`; `inspect --json` lê o resultado estruturado `v1alpha1` já gravado.
No modo `bundle`, o `--organization-id` é obrigatório e o diretório de saída
contém o manifesto, artefatos, contribuições e evidências disponíveis para
`manu ingest`.

### Plataforma local e API

Com PostgreSQL disponível, os comandos de plataforma são:

```bash
manu migrate [--format human|json|--json]
manu serve

# em outro terminal: produzir e enviar o Analysis Bundle estendido
manu analyze --root /caminho/para/fonte \
  --output /caminho/para/analysis-bundle \
  --output-mode bundle --organization-id local --source-id cliente
manu ingest --bundle /caminho/para/analysis-bundle
manu ingestion <ingestion-uuid>
manu ask --kind inventory --question 'quais artefatos existem?'
manu evidence <evidence-uuid>
manu ready
```

`manu serve` mantém o processo ativo; `migrate` aplica somente migrações
embarcadas e não faz downgrade destrutivo. `analyze --output-mode bundle`
produz o envelope estendido e exige uma organização explícita; o modo legado
continua sendo o padrão. `ingest` transmite as partes do bundle para
`POST /api/v1/ingestions`, em fluxo; `ingestion` consulta o estado do job;
`ask` cria uma consulta síncrona, `evidence` inspeciona uma unidade e `ready`
verifica a prontidão local. Os identificadores, estados e limites estão no
[contrato OpenAPI](docs/openapi.json) e em [Clientes CLI da API local](docs/cli-http.md).
A CLI aceita apenas URLs HTTP de loopback com porta explícita enquanto não
houver autenticação, recusa redirecionamentos e não imprime conteúdo bruto de
erros de transporte.

Para agentes, `MANU_MCP_ENABLED=true manu mcp` inicia o servidor MCP local por
`stdio`; consulte [Servidor MCP local](docs/mcp.md). O comando usa o
`ContextService` sobre PostgreSQL migrado e não substitui a API HTTP.

O banco canônico armazena fontes, snapshots, artefatos, contribuições,
observações, relações, evidências, cobertura, lacunas, falhas e operações. As
projeções textual, relacional e vetorial são derivadas; não há comando público
de `rebuild` neste corte. Uma nova ingestão atualiza as projeções afetadas e a
troca de perfil de embedding exige uma reconstrução explicitamente operada em
mudança própria, preservando os fatos canônicos. Não remova tabelas ou o volume
para simular reconstrução.

Embeddings e geração são capacidades independentes. Por padrão ficam
desativados, e o provedor `simulated` da avaliação não abre rede. OpenAI e o
dialeto compatível explicitamente validado com OpenRouter só são usados quando
habilitados com política de transferência, endpoint, credencial e orçamento
explícitos; endpoints OpenAI-compatible arbitrários ainda não são aceitos pelo
runtime. Não há fallback silencioso entre provedores. A prontidão local não
depende de um provedor remoto.

O conhecimento **observado** é o que a fonte/analisador sustentou com
proveniência; **gerado** é uma resposta ou síntese produzida sobre um pacote
limitado e citado; **curado** é contribuição humana revisável. A curadoria/wiki,
publicação editorial e revisão persistida ainda não fazem parte desta fatia.
Ausência de execução observada, suporte insuficiente, analisador ausente e
projeção vetorial indisponível aparecem como lacunas ou parcialidade, não como
fatos inventados.

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

# contrato e documentação
go test ./docs
git diff --check
openspec validate define-first-evidence-backed-query-slice --strict
```

`go test -race ./...` deve ser executado quando CGO e um compilador C estiverem
disponíveis. Na verificação desta mudança `CGO_ENABLED=0` e `gcc` estavam
indisponíveis, portanto o detector de corrida não foi executado. `govulncheck`
também é uma ferramenta opcional: não estava instalada e não foi instalada nem
executada, sem acesso de rede adicional.

## Imagem Linux e célula Compose

O Dockerfile usa build multi-stage com Go 1.26.6, `CGO_ENABLED=0` e runtime
`scratch`. As imagens do Agent e da plataforma são compiladas para
`linux/amd64` e `linux/arm64`; o artefato arm64 é verificado por `file` antes
de qualquer execução. O processo final usa UID/GID `65532:65532` e não possui
shell. A imagem continua podendo executar o Agent sem banco ou IA; o comando
`serve` precisa do PostgreSQL configurado.

Para a plataforma local, use a célula descrita em
[`docs/compose.md`](docs/compose.md):

```bash
cp .env.example .env
docker compose config --quiet
docker compose up --build
```

O Compose não publica PostgreSQL e expõe a API somente em loopback. Ele não
inclui MinIO, Redis, fila, worker separado, UI, autenticação ou serviço SaaS.
Detalhes de credenciais, perfis e políticas estão em
[`docs/configuration.md`](docs/configuration.md).

O Compose é Linux-first: usa rede host para manter a API em loopback e volumes
nomeados para PostgreSQL e staging. O staging usa um marcador atômico antes do
`202`; o executor recupera jobs pendentes após reinício do processo. A execução
real e as medições pontuais de consumo estão registradas em
[`docs/verification/10-3-local-cell.md`](docs/verification/10-3-local-cell.md),
sem constituir SLA ou suporte a produção.

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

Na execução do Agent em contêiner, `/source` é somente leitura e `/output` é a
única montagem gravável. O runtime não executa arquivos da fonte e não usa o
diretório da fonte para estado temporário. O volume nomeado é removível ao fim
da verificação; uma instalação local deve preservar o banco e o destino de
saída conforme sua política operacional.

## Limites do corte local

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
- O modo HTTP é experimental, sem autenticação, limitado a loopback e a uma
  organização. Não o exponha a uma rede não confiável. SaaS compartilhado,
  autenticação/autorização, daemon remoto, UI, execução local de IA e operação
  de produção não estão implementados.

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
- [`openspec/specs/knowledge-engine-runtime/spec.md`](openspec/specs/knowledge-engine-runtime/spec.md) — requisitos canônicos do runtime do Knowledge Engine.
- [`openspec/`](openspec/) — mudanças, especificações e tarefas rastreáveis.

Comece por [`AGENTS.md`](AGENTS.md) para a ordem de leitura e as regras de
trabalho.
