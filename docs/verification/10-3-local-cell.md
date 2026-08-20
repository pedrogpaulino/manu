# Verificação da célula local (10.3)

Este registro descreve a verificação local executada em 20/08/2026, no WSL2
Linux amd64. Ele não é um SLA e não contém credenciais, chaves ou conteúdo
integral de uma base.

## Ambiente e comandos

- `go version`: `go1.26.6 linux/amd64`.
- Kernel: `6.18.33.2-microsoft-standard-WSL2`, arquitetura `x86_64`.
- PostgreSQL/pgvector do Compose: `pgvector/pgvector:0.8.6-pg18-bookworm`.
- Imagem do aplicativo: `manu:local`, compilada com `CGO_ENABLED=0`.

Verificações Go executadas:

```text
GOCACHE=/tmp/manu-go-cache go test ./... -count=1
GOCACHE=/tmp/manu-go-cache go vet ./...
go mod verify
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -buildvcs=false -o /tmp/manu-linux-amd64 ./cmd/manu
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -buildvcs=false -o /tmp/manu-linux-arm64 ./cmd/manu
```

Os testes, `vet`, verificação de módulos e os dois builds estáticos terminaram
com sucesso. O build arm64 é verificado como artefato estático; a execução
operacional desta célula foi feita no host Linux amd64.

## Compose, migração e readiness

```text
docker compose config --quiet
docker compose up -d --build
docker compose ps
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:8080/readyz
docker exec manu-local-api-1 /manu ready
```

O catálogo chegou a `4/4` e `ready=true`; `migrate` terminou com código zero,
PostgreSQL ficou healthy e API ficou healthy. A API responde pelo host em
loopback com `200` nos dois endpoints. O Compose usa rede host Linux para
preservar o bind `127.0.0.1` e manter esse bind acessível ao cliente local; o
PostgreSQL continua sem porta TCP publicada. A API e o PostgreSQL compartilham
apenas o volume de socket `/var/run/postgresql`, além do staging de ingestão.

Também foi executado o teste PostgreSQL real de fila vazia:

```text
go test -tags=integration ./internal/persistence \
  -run TestPostgresIntegrationClaimEmptyQueueIsSafe -count=1
```

O teste passou em um container temporário usando o socket local e schema
isolado. A consulta de claim usa colunas qualificadas no `RETURNING`, evitando
ambiguidade no PostgreSQL 18.

Também foi executado o teste PostgreSQL real da finalização de uma consulta
abstida:

```text
go test -tags=integration ./internal/persistence \
  -run TestPostgresIntegrationPipelineFinishPersistsAbstentionWithCoherentPackageTimes -count=1
```

O teste passou com schema isolado. Ele confirma que a execução terminal, o
pacote e a resposta são persistidos antes do retorno, que `created_at` e
`finalized_at` do pacote permanecem coerentes e que uma segunda finalização
não é apresentada como sucesso.

## Staging e reinício

O serviço de ingestão aceita multipart em fluxo, publica o diretório staged de
forma atômica com marker `.ready` e cria o job PostgreSQL somente depois da
publicação. O volume nomeado `manu-ingestion-data` não é o diretório-fonte do
Agent. O teste unitário cobre leitura após recriar o stager. No ciclo
operacional, o job `b2adb43f-15ef-4272-9f67-eb26f9ecf49e` foi recuperado como
`completed` após recriar a API, com `5` artefatos, `43` observações e `22`
unidades de evidência; o staging permaneceu no volume nomeado.

O fixture estendido golden usado inicialmente para uma sondagem operacional foi
persistido no staging, mas falhou corretamente na validação canônica porque
declarava `external_transfer=allow` sem `classification=safe_text`. Nenhuma
política foi relaxada para fazer o fixture passar; a rejeição confirma
que evidência transferível sem classificação segura não é aceita. O caminho
operacional utiliza o Analysis Bundle produzido pelo Agent, com a opção
explícita `manu analyze --output-mode bundle --organization-id ...` e envio
posterior por `manu ingest`/API.

## Consulta sem provedor

Embedding e geração ficaram desabilitados e nenhum request foi enviado a
OpenAI, OpenRouter ou outro provedor. Após a ingestão válida, a consulta tipada
de abstinência foi executada:

```text
POST /api/v1/queries
{"question":"...","kind":"business_intent"}
```

A execução `1af80f47-f7d2-4494-bd28-919993ddb171` terminou em `abstained`, com
`deterministic-abstention`, provedor `none` e razão
`no_transferable_evidence`; o código de saída `3` é o resultado esperado para
essa abstinência segura. Não houve chamada externa nem resposta fabricada.

## Consumo observado

Agent, corpus local pequeno (`testdata/analyzers`), medido sem rede:

```text
/usr/bin/time -v /tmp/manu-linux-amd64 analyze \
  --root testdata/analyzers --output /tmp/manu-agent-output --json
Maximum resident set size: 11244 kbytes
Elapsed (wall clock) time: 0:00.01
```

O comando terminou com código `3` (resultado parcial esperado para o corpus),
sem impedir a medição. API e PostgreSQL, amostra única após readiness:

```text
docker stats --no-stream --format 'table {{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}\t{{.MemPerc}}\t{{.PIDs}}' \
  manu-local-api-1 manu-local-postgres-1

manu-local-api-1        0.93%   11.05MiB / 31.3GiB   0.04%   15
manu-local-postgres-1   0.72%   45.24MiB / 31.3GiB   0.12%   11
```

Esses números são uma amostra deste host/corpus, não uma promessa de consumo
para clientes. A medição não usou chave nem chamada de IA.

## Limitações explícitas

- O Compose desta célula é Linux-first por usar `network_mode: host`; a regra
  de segurança continua bloqueando qualquer bind não-loopback.
- O fixture golden incompatível permanece um caso negativo deliberado: ele
  não pode ser usado para simular um fluxo Agent → API porque viola a política
  de classificação segura. O caminho bundle explícito é o contrato usado no
  ciclo válido.
- A consulta sem provedor depende de um snapshot válido ativado pela ingestão;
  readiness de banco/schema sozinho não satisfaz essa pré-condição.
