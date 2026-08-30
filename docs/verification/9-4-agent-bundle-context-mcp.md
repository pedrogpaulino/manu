# Verificação integrada Agent → bundle → contexto → MCP (tarefa 9.4)

Este registro documenta a execução local de 2026-08-29 na revisão `9667227`.
Ela valida o caminho operacional do Agent até uma consulta MCP, sem representar
um SLA, uma avaliação de qualidade geral ou uma operação de produção.

## Ambiente e preparação

- Linux amd64, Docker Compose local e imagem do Manu reconstruída.
- `docker compose config --quiet` passou.
- A migração chegou ao schema `6`; PostgreSQL e API ficaram saudáveis.
- `/healthz` e `/readyz` responderam `200`.
- O MCP usou `stdio`, protocolo `2025-11-25`, servidor `manu dev` e sessão
  local somente leitura.

Embedding, geração, provedor, modelo e rede externa permaneceram desabilitados.
A política observada foi `persist=allow` e `external_transfer=deny`; nenhuma
credencial, conteúdo integral da fonte ou chamada externa foi usado.

## Fluxo executado

O Agent produziu o bundle estendido a partir da fixture local com:

```text
/tmp/manu-linux-amd64 analyze \
  --root testdata/analyzers \
  --output /tmp/manu-flow-9-4.04PYxu/bundle \
  --output-mode bundle \
  --organization-id local \
  --source-id verification-9-4 \
  --json
```

O resultado do Agent foi parcial, como esperado para os analisadores limitados,
mas o bundle foi aceito pela ingestão. O job
`f19be932-3157-4148-89c8-3ec67bc9513d` terminou com ativação `completed`,
tentativa `1`, `5` artefatos, `49` observações, `23` evidências e `0` falhas.

O escopo UUID ativado foi:

| Identidade | UUID |
| --- | --- |
| Organization | `2bc921fa-dedb-5829-bea3-d5c59d3b4736` |
| Source | `0986c58c-ac12-5964-b213-5ac5e7b84f01` |
| Snapshot | `250c69ad-fbb8-5784-9c74-75f0ec7a3bfd` |

Com esse escopo, `manu_query` recebeu a pergunta `Booking` e o orçamento de
`2000` tokens, `2` itens, `4000` caracteres e `8000` bytes. A
resposta foi bem-sucedida e registrou:

- revisão `104e459cd22287f52aebd9aae6aaf67a363b5fef22376ae43e70306b98202163`;
- `2` itens de evidência, com locadores em `Sample.java`, linhas `13` e `22`;
- trechos curtos de código efetivamente retornados;
- `3525` bytes/caracteres usados, estimativa de `882` tokens e `truncated=false`.

## Limites observados

- A recuperação foi sensível aos termos: consultas anteriores por `Sample` e
  `BookingService` não retornaram itens; não se deve interpretar uma consulta
  vazia como cobertura completa.
- O resultado sinalizou `vector_unavailable` e `coverage_incomplete`.
- Permaneceram lacunas de Java lexical, dinâmica WSO2, limites de evidência e
  redaction.
- O MCP continua local, por `stdio`, somente leitura e sem autenticação de
  produção ou transporte remoto. O cliente deve manter a sessão de entrada
  aberta enquanto aguarda as respostas do protocolo.

Assim, o fluxo Agent → bundle → ingestão → ContextService → MCP está comprovado
para esta fixture e revisão, com conteúdo citado e limitado. A evidência não
autoriza inferir compreensão semântica completa, execução observada, uso de
modelo ou generalização para outras fontes.

## Verificações finais

Na mesma revisão, os gates finais passaram: `gofmt -d` não encontrou diferenças;
`go vet ./...`, `go test ./... -count=1`, `go mod verify`, `go test ./docs`,
`git diff --check`, `docker compose config --quiet` e
`openspec validate establish-evidence-backed-context-engine --strict` foram
concluídos com sucesso.

Os builds `GOOS=linux GOARCH=amd64 CGO_ENABLED=0` e
`GOOS=linux GOARCH=arm64 CGO_ENABLED=0` de `./cmd/manu` foram produzidos e
confirmados como estaticamente vinculados; `ldd` informou `not a dynamic
executable` para o artefato amd64. O ambiente tinha `CGO_ENABLED=0` e não tinha
compilador C (`cc` ausente), portanto `go test -race ./...` não foi executado.
`govulncheck` também estava ausente e a verificação de vulnerabilidades não foi
executada.
